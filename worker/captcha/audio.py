"""
captcha/audio.py
================
reCAPTCHA audio challenge download + transcription.

Backends (configurable via config.yaml captcha.solver):
  - "google_web":   SpeechRecognition Google Web Speech API (free, undocumented, rate-limited)
  - "whisper":      OpenAI Whisper local model (large-v3 by default, no network needed)
  - "google_cloud": Google Cloud Speech-to-Text (requires GOOGLE_APPLICATION_CREDENTIALS env)

Flow:
  1. Find <audio> src in reCAPTCHA bframe iframe
  2. Download audio bytes via browser fetch (goes through proxy)
  3. Convert MP3 -> WAV (mono, 16kHz) with pydub/ffmpeg
  4. Transcribe with selected backend
  5. Convert spoken numbers to digits ("one two three" -> "1 2 3")
"""

from __future__ import annotations

import asyncio
import base64
import io
import logging
import os
import re
import tempfile
from typing import Optional

logger = logging.getLogger("worker.captcha.audio")

_WORD_TO_DIGIT = {
    "zero": "0", "one": "1", "two": "2", "three": "3", "four": "4",
    "five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
}

DEFAULT_BACKEND = "google_web"
DEFAULT_WHISPER_MODEL = "large-v3"

_whisper_model_cache = None


def _get_configured_backend() -> str:
    """Read solver backend from config.yaml captcha.solver."""
    try:
        import yaml
        config_path = os.path.expanduser("~/Project/google-automation/config/config.yaml")
        with open(config_path, "r") as f:
            cfg = yaml.safe_load(f)
        captcha_cfg = cfg.get("captcha", {})
        return captcha_cfg.get("solver", DEFAULT_BACKEND)
    except Exception:
        return DEFAULT_BACKEND


def _get_whisper_model_name() -> str:
    """Read whisper model size from config.yaml captcha.whisper_model."""
    try:
        import yaml
        config_path = os.path.expanduser("~/Project/google-automation/config/config.yaml")
        with open(config_path, "r") as f:
            cfg = yaml.safe_load(f)
        captcha_cfg = cfg.get("captcha", {})
        return captcha_cfg.get("whisper_model", DEFAULT_WHISPER_MODEL)
    except Exception:
        return DEFAULT_WHISPER_MODEL


async def download_audio(frame) -> Optional[bytes]:
    """Download reCAPTCHA audio from the bframe iframe via browser fetch."""
    audio_src = await frame.evaluate("""
        () => {
            const audio = document.querySelector('audio');
            if (audio && audio.src) return audio.src;
            const source = document.querySelector('audio source');
            if (source && source.src) return source.src;
            return null;
        }
    """)

    if not audio_src:
        logger.error("No audio source found in bframe")
        return None

    logger.info("Audio source: %s", audio_src[:120])

    data_url = await frame.evaluate("""
        async (url) => {
            try {
                const response = await fetch(url);
                if (!response.ok) return null;
                const blob = await response.blob();
                return new Promise((resolve) => {
                    const reader = new FileReader();
                    reader.onload = () => resolve(reader.result);
                    reader.onerror = () => resolve(null);
                    reader.readAsDataURL(blob);
                });
            } catch (e) {
                return null;
            }
        }
    """, audio_src)

    if not data_url or not data_url.startswith("data:"):
        logger.error("Failed to download audio via fetch")
        return None

    base64_data = data_url.split(",", 1)[1]
    audio_bytes = base64.b64decode(base64_data)
    logger.info("Downloaded audio: %d bytes", len(audio_bytes))
    return audio_bytes


def transcribe_audio(audio_bytes: bytes, backend: str = None) -> Optional[str]:
    """
    Transcribe audio bytes to text using the specified backend.

    Args:
        audio_bytes: Raw audio data (MP3 or WAV)
        backend: "google_web", "whisper", "google_cloud", or None (read from config)

    Returns cleaned text or None.
    """
    if backend is None:
        backend = _get_configured_backend()
    logger.info("Transcribing audio with backend: %s", backend)

    wav_path = _convert_to_wav(audio_bytes)
    if not wav_path:
        return None

    try:
        if backend == "whisper":
            raw = _transcribe_whisper(wav_path)
        elif backend == "google_cloud":
            raw = _transcribe_google_cloud(wav_path)
        else:
            raw = _transcribe_google_web(wav_path)

        if not raw:
            return None

        text = _clean_transcription(raw)
        logger.info("Transcription: '%s' (raw: '%s', backend: %s)", text, raw, backend)
        return text
    finally:
        try:
            os.unlink(wav_path)
        except OSError:
            pass


def _convert_to_wav(audio_bytes: bytes) -> Optional[str]:
    """Convert audio bytes to WAV (mono, 16kHz) using pydub."""
    try:
        from pydub import AudioSegment
    except ImportError:
        logger.error("pydub not installed — pip install pydub")
        return None

    try:
        audio = AudioSegment.from_file(io.BytesIO(audio_bytes))
        audio = audio.set_channels(1).set_frame_rate(16000)

        with tempfile.NamedTemporaryFile(suffix=".wav", delete=False) as f:
            wav_path = f.name
            audio.export(wav_path, format="wav")
        return wav_path
    except Exception as e:
        logger.error("Audio conversion failed: %s", e)
        return None


# ---------------------------------------------------------------------------
# Backend: Google Web Speech API (free, undocumented, rate-limited)
# ---------------------------------------------------------------------------

def _transcribe_google_web(wav_path: str) -> Optional[str]:
    """Transcribe using Google Web Speech API via SpeechRecognition."""
    try:
        import speech_recognition as sr
    except ImportError:
        logger.error("SpeechRecognition not installed — pip install SpeechRecognition")
        return None

    r = sr.Recognizer()
    try:
        with sr.AudioFile(wav_path) as source:
            audio_data = r.record(source)
    except Exception as e:
        logger.error("Failed to read audio file: %s", e)
        return None

    try:
        return r.recognize_google(audio_data, language="en-US")
    except sr.UnknownValueError:
        logger.warning("Google Web Speech could not understand audio")
        return None
    except sr.RequestError as e:
        logger.error("Google Web Speech API error: %s", e)
        return None


# ---------------------------------------------------------------------------
# Backend: OpenAI Whisper (local, no network, large-v3 default)
# ---------------------------------------------------------------------------

def _get_whisper_model():
    """Load and cache Whisper model (lazy loading, first call downloads weights)."""
    global _whisper_model_cache
    if _whisper_model_cache is not None:
        return _whisper_model_cache

    try:
        import whisper
    except ImportError:
        logger.error("openai-whisper not installed — pip install openai-whisper")
        return None

    model_name = _get_whisper_model_name()
    logger.info("Loading Whisper model: %s (first load downloads ~3GB weights)", model_name)
    _whisper_model_cache = whisper.load_model(model_name)
    logger.info("Whisper model loaded: %s", model_name)
    return _whisper_model_cache


def _transcribe_whisper(wav_path: str) -> Optional[str]:
    """Transcribe using OpenAI Whisper local model."""
    model = _get_whisper_model()
    if model is None:
        return None

    try:
        result = model.transcribe(wav_path, language="en", fp16=False)
        return result.get("text", "").strip()
    except Exception as e:
        logger.error("Whisper transcription error: %s", e)
        return None


# ---------------------------------------------------------------------------
# Backend: Google Cloud Speech-to-Text (requires API key)
# ---------------------------------------------------------------------------

def _transcribe_google_cloud(wav_path: str) -> Optional[str]:
    """Transcribe using Google Cloud Speech-to-Text API."""
    try:
        import speech_recognition as sr
    except ImportError:
        logger.error("SpeechRecognition not installed")
        return None

    if not os.environ.get("GOOGLE_APPLICATION_CREDENTIALS"):
        logger.error("GOOGLE_APPLICATION_CREDENTIALS not set — cannot use Google Cloud Speech-to-Text")
        return None

    r = sr.Recognizer()
    try:
        with sr.AudioFile(wav_path) as source:
            audio_data = r.record(source)
    except Exception as e:
        logger.error("Failed to read audio file: %s", e)
        return None

    try:
        return r.recognize_google_cloud(audio_data, language="en-US")
    except sr.UnknownValueError:
        logger.warning("Google Cloud Speech could not understand audio")
        return None
    except sr.RequestError as e:
        logger.error("Google Cloud Speech API error: %s", e)
        return None
    except Exception as e:
        logger.error("Google Cloud Speech error: %s", e)
        return None


# ---------------------------------------------------------------------------
# Text cleaning
# ---------------------------------------------------------------------------

def _clean_transcription(text: str) -> str:
    """Clean transcription: strip punctuation, convert number words to digits."""
    text = re.sub(r'[^a-zA-Z0-9\s]', '', text).strip().lower()

    words = text.split()
    converted = []
    for word in words:
        converted.append(_WORD_TO_DIGIT.get(word, word))

    result = " ".join(converted)
    if not result:
        return text
    return result
