package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	quickjswasi "github.com/paralin/go-quickjs-wasi"
)

// ExtractionRequest specifies the parameters for a residential extraction job
type ExtractionRequest struct {
	URL           string `json:"url"`
	Country       string `json:"country,omitempty"`        // Target country code (e.g. "US", "DE")
	ExtractScript string `json:"extract_script,omitempty"` // Custom JS extraction function
	TimeoutSec    int    `json:"timeout_sec,omitempty"`
}

// ExtractionResult holds the structured payload returned to the client
type ExtractionResult struct {
	Success    bool            `json:"success"`
	URL        string          `json:"url"`
	StatusCode int             `json:"status_code"`
	NodeGeo    NodeGeoInfo     `json:"node_geo"`
	LatencyMs  int64           `json:"latency_ms"`
	Data       json.RawMessage `json:"data"`
	Error      string          `json:"error,omitempty"`
}

// PrepareExtractionScript bundles the HTML, URL, DOM helper utilities, and user script into a QuickJS executable payload
func PrepareExtractionScript(targetURL string, htmlBody string, statusCode int, userScript string) string {
	escapedURL, _ := json.Marshal(targetURL)
	escapedHTML, _ := json.Marshal(htmlBody)

	// If no custom script is specified, use the intelligent default AI content extractor
	if strings.TrimSpace(userScript) == "" || userScript == "default" {
		userScript = defaultExtractorJS
	}

	return fmt.Sprintf(`
(function() {
	const __URL__ = %s;
	const __HTML__ = %s;
	const __STATUS__ = %d;

	// Lightweight DOM / Micro-Query Helpers for Sandboxed Extraction
	function stripTags(html) {
		return html.replace(/<style[^>]*>[\s\S]*?<\/style>/gi, '')
		           .replace(/<script[^>]*>[\s\S]*?<\/script>/gi, '')
		           .replace(/<svg[^>]*>[\s\S]*?<\/svg>/gi, '')
		           .replace(/<!--[\s\S]*?-->/g, '')
		           .replace(/<[^>]+>/g, ' ')
		           .replace(/\s+/g, ' ')
		           .trim();
	}

	function matchTag(html, tag) {
		const re = new RegExp('<' + tag + '[^>]*>([\\s\\S]*?)<\\/' + tag + '>', 'i');
		const m = html.match(re);
		return m ? stripTags(m[1]) : '';
	}

	function matchAllTags(html, tag) {
		const re = new RegExp('<' + tag + '[^>]*>([\\s\\S]*?)<\\/' + tag + '>', 'gi');
		const results = [];
		let m;
		while ((m = re.exec(html)) !== null) {
			const clean = stripTags(m[1]);
			if (clean.length > 0) results.push(clean);
		}
		return results;
	}

	function matchMeta(html, nameOrProp) {
		const re1 = new RegExp('<meta[^>]+(?:name|property)=["\']' + nameOrProp + '["\'][^>]+content=["\']([^"\']*)["\']', 'i');
		const re2 = new RegExp('<meta[^>]+content=["\']([^"\']*)["\'][^>]+(?:name|property)=["\']' + nameOrProp + '["\']', 'i');
		const m = html.match(re1) || html.match(re2);
		return m ? m[1] : '';
	}

	function extractLinks(html) {
		const re = /<a[^>]+href=["']([^"'#][^"']*)["'][^>]*>([\s\S]*?)<\/a>/gi;
		const links = [];
		let m;
		while ((m = re.exec(html)) !== null && links.length < 50) {
			const text = stripTags(m[2]);
			if (m[1] && !m[1].startsWith('javascript:')) {
				links.push({ href: m[1], text: text });
			}
		}
		return links;
	}

	// Environment exposed to extraction script
	const env = {
		url: __URL__,
		html: __HTML__,
		status: __STATUS__,
		stripTags: stripTags,
		matchTag: matchTag,
		matchAllTags: matchAllTags,
		matchMeta: matchMeta,
		extractLinks: extractLinks
	};

	try {
		const extractorFn = %s;
		let result;
		if (typeof extractorFn === 'function') {
			result = extractorFn(env);
		} else {
			result = extractorFn;
		}
		console.log(JSON.stringify(result));
	} catch (err) {
		console.log(JSON.stringify({ error: "Script execution error: " + err.message }));
	}
})();
`, string(escapedURL), string(escapedHTML), statusCode, wrapUserScript(userScript))
}

func wrapUserScript(script string) string {
	trimmed := strings.TrimSpace(script)
	if strings.HasPrefix(trimmed, "function") || strings.HasPrefix(trimmed, "(") {
		return trimmed
	}
	// Wrap statements into an anonymous function
	return fmt.Sprintf("function(ctx) { %s }", script)
}

const defaultExtractorJS = `function(ctx) {
	return {
		title: ctx.matchTag(ctx.html, 'title'),
		description: ctx.matchMeta(ctx.html, 'description') || ctx.matchMeta(ctx.html, 'og:description'),
		og_title: ctx.matchMeta(ctx.html, 'og:title'),
		og_image: ctx.matchMeta(ctx.html, 'og:image'),
		canonical: ctx.matchMeta(ctx.html, 'og:url'),
		headings: {
			h1: ctx.matchAllTags(ctx.html, 'h1'),
			h2: ctx.matchAllTags(ctx.html, 'h2').slice(0, 10)
		},
		clean_text_preview: ctx.stripTags(ctx.html).substring(0, 2000),
		links_count: ctx.extractLinks(ctx.html).length,
		sample_links: ctx.extractLinks(ctx.html).slice(0, 10)
	};
}`

// ExecuteExtractionSandbox runs the prepared JS script inside the QuickJS WASI sandbox
func ExecuteExtractionSandbox(ctx context.Context, jsCode string) ([]byte, error) {
	var outBuf bytes.Buffer
	args := []string{"qjs", "-e", jsCode}

	wasmBytes := quickjswasi.QuickJSWASM
	duration, err := executeWasm(ctx, wasmBytes, []byte(jsCode), &outBuf, args)
	_ = duration

	if err != nil {
		return nil, fmt.Errorf("sandbox execution error: %w", err)
	}

	output := bytes.TrimSpace(outBuf.Bytes())
	if len(output) == 0 {
		return []byte(`{"error": "sandbox produced empty output"}`), nil
	}

	return output, nil
}
