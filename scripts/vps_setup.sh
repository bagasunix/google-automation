#!/bin/bash
# VPS Setup Script for Google Automation on Ubuntu / Debian
# Run once on your VPS: bash scripts/vps_setup.sh

set -e

echo "=== Google Automation VPS Setup ==="

# 1. Update system packages
sudo apt update && sudo apt install -y \
    curl wget git build-essential \
    python3 python3-venv python3-pip \
    libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 \
    libxcomposite1 libxdamage1 libxfixes3 libxrandr2 libgbm1 \
    libasound2 libpango-1.0-0 libpangocairo-1.0-0 \
    xvfb unzip

# 2. Install Google Chrome Stable (if not installed)
if ! command -v google-chrome &>/dev/null; then
    echo "Installing Google Chrome..."
    wget -q -O - https://dl-ssl.google.com/linux/linux_signing_key.pub | sudo gpg --dearmor -o /etc/apt/trusted.gpg.d/google.gpg
    echo "deb [arch=amd64] http://dl.google.com/linux/chrome/deb/ stable main" | sudo tee /etc/apt/sources.list.d/google-chrome.list
    sudo apt update
    sudo apt install -y google-chrome-stable
fi

# 3. Setup Python Virtual Environment
echo "Setting up Python environment..."
cd worker
python3 -m venv .venv
source .venv/bin/activate
pip install --upgrade pip
pip install -r requirements.txt
python -m seleniumbase install chromedriver
cd ..

# 4. Install Go toolchain
# go.mod requires 1.25.x, which is newer than the golang package on most
# Debian/Ubuntu releases — so install the official tarball rather than apt.
GO_VERSION="1.25.14"
if ! command -v go &>/dev/null && [ ! -x /usr/local/go/bin/go ]; then
    echo "Installing Go ${GO_VERSION}..."
    case "$(uname -m)" in
        x86_64)  GO_ARCH=amd64 ;;
        aarch64) GO_ARCH=arm64 ;;
        *) echo "ERROR: unsupported architecture $(uname -m)" >&2; exit 1 ;;
    esac
    wget -q -O /tmp/go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf /tmp/go.tar.gz
    rm -f /tmp/go.tar.gz
    # Persist for future logins as well as this script.
    echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh > /dev/null
fi
export PATH="$PATH:/usr/local/go/bin"

if ! command -v go &>/dev/null; then
    echo "ERROR: Go is still not on PATH after installation." >&2
    exit 1
fi
echo "Go: $(go version)"

# 5. Build Go Binaries
#
# This build is MANDATORY, not best-effort. bin/dashboard and bin/orchestrator
# are still tracked in git, so a fresh clone ships prebuilt binaries — and the
# systemd units run those files by path. When this step was wrapped in
# `if command -v go`, a VPS without Go skipped the build silently and systemd
# then ran the stale committed binary instead of the code just cloned.
echo "Building Go orchestrator & dashboard..."
go build -o bin/orchestrator cmd/main.go
go build -o bin/dashboard cmd/dashboard/main.go
echo "Go binaries built in ./bin"

# 6. Credentials
if [ ! -f .env ]; then
    echo ""
    echo "WARNING: .env not found. It is intentionally not in git, so create it"
    echo "         from .env.example before starting the services — the dashboard"
    echo "         now refuses to start without DASHBOARD_USERNAME/PASSWORD."
fi

echo "=== Setup Completed! ==="
echo "To start: ./scripts/run.sh"
echo "To run dashboard server: ./bin/dashboard --serve :8080"
