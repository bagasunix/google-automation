"""
paths.py
========
Single source of truth for on-disk locations.

Every module used to hardcode os.path.expanduser("~/Project/google-automation").
That resolves to a directory that does not exist on any machine where the repo
lives somewhere else — a VPS deploy under /opt or /srv, or simply a different
user's home. Worse, every caller wrapped the lookup in `except Exception:
return {}`, so a wrong path raised nothing and produced silently-empty config:
captcha.openai_base_url disappears (sending a Groq key to api.openai.com, which
401s), bandwidth blocking rules vanish, the token-solver key is ignored, warm
Chrome profiles get created in a stray location and lose their accumulated
cookies. Deriving the root from this file's own location keeps all of that
correct wherever the repo is checked out.
"""

import os

# worker/paths.py -> worker/ -> repo root
BASE_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

CONFIG_PATH = os.path.join(BASE_DIR, "config", "config.yaml")
ENV_PATH = os.path.join(BASE_DIR, ".env")
PROFILES_DIR = os.path.join(BASE_DIR, "data", "profiles")
