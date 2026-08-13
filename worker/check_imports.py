"""Import check — dijalankan oleh CI untuk verifikasi semua modul bisa di-import."""
import sys
import os

# Tambah worker/ ke sys.path
sys.path.insert(0, os.path.dirname(__file__))

from browser.session import StealthSession, create_session
from browser import stealth, humanizer
from search import google, bing, serp
from engagement import dwell, click, exit as exit_mod
from reporter import build_result, save_result_json, capture_screenshot
from main import WorkerServiceServicer, run_grpc_server, main

print("All Python imports OK")
