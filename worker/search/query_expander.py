"""
search/query_expander.py
========================
AI Semantic Query Expander using Groq / OpenAI LLM + Heuristic Linguistic Fallback.
Generates humanized Indonesian search query variations based on article title and topic.
"""

from __future__ import annotations

import json
import logging
import os
import random
import re
from typing import List

logger = logging.getLogger("worker.search.query_expander")


def _load_config() -> dict:
    try:
        import yaml
        config_path = os.path.expanduser("~/Project/google-automation/config/config.yaml")
        with open(config_path, "r") as f:
            return yaml.safe_load(f) or {}
    except Exception:
        return {}


def expand_queries_heuristic(title: str, topic: str = "", domain: str = "") -> List[str]:
    """
    Generate heuristic query variations without requiring an external LLM API.
    Patterns:
      1. Cara / How-to: 'cara [title]'
      2. Tutorial / Panduan: '[title] tutorial bahasa indonesia'
      3. Problem solving: 'solusi error [topic/title]'
      4. Indonesian slang / colloquial: 'gmn cara setting [title]'
      5. Branded search: '[title] [brand]'
    """
    clean_title = re.sub(r'[\-\|\–].*$', '', title).strip().lower()
    clean_title = re.sub(r'[^a-zA-Z0-9\s]', '', clean_title)

    words = clean_title.split()
    short_title = " ".join(words[:5]) if len(words) > 5 else clean_title

    brand = domain.replace("www.", "").split(".")[0] if domain else "bagasunix"

    variations = [
        title,
        f"cara {clean_title}",
        f"tutorial {short_title} lengkap",
        f"panduan {clean_title} bahasa indonesia",
        f"gmn cara {short_title}",
        f"{short_title} {brand}",
    ]

    if topic:
        clean_topic = topic.strip().lower()
        variations.append(f"{clean_topic} {short_title}")
        variations.append(f"tips {clean_topic} {brand}")

    return list(dict.fromkeys(variations))


def expand_queries_ai(title: str, topic: str = "", domain: str = "", max_queries: int = 6) -> List[str]:
    """
    Use Groq / OpenAI LLM to generate semantic, human-like Indonesian search variations.
    Falls back to heuristic rules if API is not configured or fails.
    """
    cfg = _load_config()
    captcha_cfg = cfg.get("captcha", {})
    api_key = (
        captcha_cfg.get("openai_api_key")
        or os.environ.get("OPENAI_API_KEY")
        or os.environ.get("GROQ_API_KEY")
    )
    base_url = captcha_cfg.get("openai_base_url") or "https://api.groq.com/openai/v1"
    model = "llama-3.1-8b-instant" if "groq" in base_url else "gpt-4o-mini"

    if not api_key:
        logger.debug("No API key for AI Query Expander — using heuristic generator")
        return expand_queries_heuristic(title, topic, domain)

    try:
        from openai import OpenAI
        client = OpenAI(api_key=api_key, base_url=base_url)

        brand = domain.replace("www.", "").split(".")[0] if domain else "bagasunix"
        prompt = f"""Kamu adalah pakar SEO dan pengguna Google di Indonesia.
Diberikan judul artikel: "{title}"
Topik: "{topic}"
Domain/Brand: "{brand}"

Buat {max_queries} variasi kata kunci / query pencarian Google yang SANGAT ALAMI diketik orang Indonesia (kombinasi pertanyaan, bahasa santai/singkatan 'gmn/cara', tutorial, dan pencarian branded).
Output HANYA format JSON Array string tanpa markdown, contoh:
["cara ...", "gmn cara ...", "... tutorial", "... {brand}"]"""

        resp = client.chat.completions.create(
            model=model,
            messages=[{"role": "user", "content": prompt}],
            temperature=0.7,
            max_tokens=250,
        )

        content = resp.choices[0].message.content.strip()
        # Parse JSON
        if content.startswith("```"):
            content = re.sub(r'^```(?:json)?\s*', '', content)
            content = re.sub(r'\s*```$', '', content)

        queries = json.loads(content)
        if isinstance(queries, list) and len(queries) > 0:
            logger.info("AI Query Expander generated %d semantic queries for %s", len(queries), title[:40])
            return queries
    except Exception as e:
        logger.warning("AI Query Expander API call failed (%s) — using heuristic fallback", e)

    return expand_queries_heuristic(title, topic, domain)
