"""
Emdash Python Quickstart for AI Agents
Install requests: pip install requests
"""
import requests

API_ENDPOINT = "http://localhost:8080/api/extract?sync=true"
API_KEY = "emdash_trial_demo"

def extract_web_page(url: str, country: str = "US"):
    headers = {
        "Authorization": f"Bearer {API_KEY}",
        "Content-Type": "application/json"
    }
    payload = {
        "url": url,
        "country": country
    }
    
    response = requests.post(API_ENDPOINT, json=payload, headers=headers)
    response.raise_for_status()
    
    result = response.json()
    print(f"Extraction Status: {result['status_code']} | Latency: {result['latency_ms']}ms")
    print(f"Page Title: {result['data']['title']}")
    print(f"Clean Content Preview:\n{result['data']['clean_text_preview'][:300]}...")
    return result["data"]

if __name__ == "__main__":
    extract_web_page("https://news.ycombinator.com")
