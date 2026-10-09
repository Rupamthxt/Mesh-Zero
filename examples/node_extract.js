/**
 * Emdash Node.js Quickstart for AI Agents
 * Native fetch in Node 18+
 */

const API_ENDPOINT = "http://localhost:8080/api/extract?sync=true";
const API_KEY = "emdash_trial_demo";

async function extractWebPage(url, country = "US") {
  const response = await fetch(API_ENDPOINT, {
    method: "POST",
    headers: {
      "Authorization": `Bearer ${API_KEY}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ url, country }),
  });

  if (!response.ok) {
    throw new Error(`Extraction failed: ${response.statusText}`);
  }

  const result = await response.json();
  console.log(`Status: ${result.status_code} | Latency: ${result.latency_ms}ms`);
  console.log("Title:", result.data.title);
  console.log("Clean Text Preview:", result.data.clean_text_preview.slice(0, 200));
  return result.data;
}

extractWebPage("https://news.ycombinator.com").catch(console.error);
