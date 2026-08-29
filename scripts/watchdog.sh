#!/bin/bash
# VPS Watchdog & Auto-Heal Script for Google Automation
# Can be run via cron every 10 minutes: */10 * * * * /path/to/scripts/watchdog.sh

set -e

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
LOG_FILE="$PROJECT_DIR/data/watchdog.log"
mkdir -p "$PROJECT_DIR/data"

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" >> "$LOG_FILE"
}

# 1. Check if orchestrator or worker crashed while service should be active
if systemctl is-active --quiet google-automation.service 2>/dev/null; then
    # Check for zombie / leaked chrome processes (> 2 hours old without parent)
    ZOMBIES=$(pgrep -f "chrome.*--headless=new" || true)
    if [ -n "$ZOMBIES" ]; then
        COUNT=$(echo "$ZOMBIES" | wc -w)
        if [ "$COUNT" -gt 15 ]; then
            log "WARNING: High number of Chrome instances ($COUNT). Cleaning up orphaned processes..."
            pkill -f "chrome.*--headless=new" 2>/dev/null || true
            sleep 2
            systemctl restart google-automation.service
            log "Service google-automation restarted after cleanup."
        fi
    fi
fi

# 2. Check disk space inside data/profiles
PROFILES_DIR="$PROJECT_DIR/data/profiles"
if [ -d "$PROFILES_DIR" ]; then
    SIZE_MB=$(du -sm "$PROFILES_DIR" | awk '{print $1}')
    if [ "$SIZE_MB" -gt 2048 ]; then
        log "Profiles folder exceeded 2GB ($SIZE_MB MB). Cleaning up cache..."
        find "$PROFILES_DIR" -type d -name "Cache" -exec rm -rf {} + 2>/dev/null || true
        find "$PROFILES_DIR" -type d -name "Code Cache" -exec rm -rf {} + 2>/dev/null || true
    fi
fi
