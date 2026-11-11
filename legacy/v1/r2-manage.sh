#!/bin/bash
# R2 Management Script for GroveMC
# Handles mod deletion, upload, and world reset operations
#
# Prerequisites:
#   1. Install rclone: brew install rclone
#   2. Configure rclone with R2 credentials (see setup instructions below)
#
# Usage:
#   ./r2-manage.sh list-mods              - List all mods in R2
#   ./r2-manage.sh delete-all-mods        - Delete ALL mods from R2
#   ./r2-manage.sh upload-mods <path>     - Upload new mods from folder
#   ./r2-manage.sh list-world             - List world files in R2
#   ./r2-manage.sh delete-world           - Delete world data from R2
#   ./r2-manage.sh setup                  - Configure rclone for R2

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKIP_LIST="$SCRIPT_DIR/client-only-mods.txt"
R2_REMOTE="r2grove"  # rclone remote name

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_header() {
    echo -e "${BLUE}=== $1 ===${NC}"
}

print_success() {
    echo -e "${GREEN}✓ $1${NC}"
}

print_warning() {
    echo -e "${YELLOW}⚠ $1${NC}"
}

print_error() {
    echo -e "${RED}✗ $1${NC}"
}

check_rclone() {
    if ! command -v rclone &> /dev/null; then
        print_error "rclone is not installed!"
        echo ""
        echo "Install with: brew install rclone"
        echo "Then run: ./r2-manage.sh setup"
        exit 1
    fi

    # Check if remote is configured
    if ! rclone listremotes | grep -q "^${R2_REMOTE}:"; then
        print_error "rclone remote '${R2_REMOTE}' is not configured!"
        echo ""
        echo "Run: ./r2-manage.sh setup"
        exit 1
    fi

    # Verify we can actually access the bucket
    if ! rclone lsd ${R2_REMOTE}:mc-assets &>/dev/null; then
        print_error "Cannot access mc-assets bucket. Check your credentials."
        exit 1
    fi
}

setup_rclone() {
    print_header "R2 rclone Setup"

    if ! command -v rclone &> /dev/null; then
        echo "Installing rclone via Homebrew..."
        brew install rclone
    fi

    # Check for credentials file
    CREDS_FILE="$SCRIPT_DIR/r2-credentials.json"
    if [ -f "$CREDS_FILE" ]; then
        echo "Found r2-credentials.json, reading..."

        # Parse JSON (using python since it's available on macOS)
        CF_ACCOUNT_ID=$(python3 -c "import json; print(json.load(open('$CREDS_FILE'))['account_id'])" 2>/dev/null)
        R2_ACCESS_KEY=$(python3 -c "import json; print(json.load(open('$CREDS_FILE'))['access_key_id'])" 2>/dev/null)
        R2_SECRET_KEY=$(python3 -c "import json; print(json.load(open('$CREDS_FILE'))['secret_access_key'])" 2>/dev/null)

        if [ "$CF_ACCOUNT_ID" == "YOUR_CLOUDFLARE_ACCOUNT_ID" ] || [ -z "$CF_ACCOUNT_ID" ]; then
            print_error "Please fill in r2-credentials.json with your actual credentials"
            echo ""
            echo "Get them from: Cloudflare Dashboard > R2 > Manage R2 API Tokens"
            exit 1
        fi

        echo "Account ID: ${CF_ACCOUNT_ID:0:8}..."
        echo "Access Key: ${R2_ACCESS_KEY:0:8}..."
    else
        echo ""
        echo "No credentials file found."
        echo "Please fill in r2-credentials.json with:"
        echo "  - account_id: Your Cloudflare Account ID"
        echo "  - access_key_id: R2 API token Access Key"
        echo "  - secret_access_key: R2 API token Secret"
        echo ""
        echo "Get these from: Cloudflare Dashboard > R2 > Manage R2 API Tokens"
        exit 1
    fi

    # Ensure config directory exists
    mkdir -p ~/.config/rclone

    # Remove existing r2grove config if present
    if [ -f ~/.config/rclone/rclone.conf ]; then
        # Remove existing r2grove section
        sed -i '' '/^\[r2grove\]/,/^\[/{ /^\[r2grove\]/d; /^\[/!d; }' ~/.config/rclone/rclone.conf 2>/dev/null || true
    fi

    # Add new config
    cat >> ~/.config/rclone/rclone.conf << EOF

[${R2_REMOTE}]
type = s3
provider = Cloudflare
access_key_id = ${R2_ACCESS_KEY}
secret_access_key = ${R2_SECRET_KEY}
endpoint = https://${CF_ACCOUNT_ID}.r2.cloudflarestorage.com
acl = private
EOF

    print_success "rclone configured!"
    echo ""
    echo "Testing connection..."
    # Try listing mc-assets (since token might be bucket-scoped)
    if rclone lsd ${R2_REMOTE}:mc-assets 2>/dev/null; then
        print_success "Connection successful! Found in mc-assets:"
        rclone lsd ${R2_REMOTE}:mc-assets | while read line; do
            echo "  $line"
        done
    else
        print_error "Connection failed - check your credentials"
        exit 1
    fi
}

list_mods() {
    check_rclone
    print_header "Mods in R2 (mc-assets/mods/)"

    local count=$(rclone ls ${R2_REMOTE}:mc-assets/mods/ 2>/dev/null | wc -l | tr -d ' ')
    rclone ls ${R2_REMOTE}:mc-assets/mods/ 2>/dev/null | while read size name; do
        echo "  ${name} ($(numfmt --to=iec-i --suffix=B ${size} 2>/dev/null || echo "${size} B"))"
    done

    echo ""
    print_success "Total: ${count} mods"
}

delete_all_mods() {
    check_rclone
    print_header "Delete ALL Mods from R2"

    local count=$(rclone ls ${R2_REMOTE}:mc-assets/mods/ 2>/dev/null | wc -l | tr -d ' ')

    if [ "$count" -eq 0 ]; then
        print_warning "No mods found in R2"
        exit 0
    fi

    print_warning "This will delete ${count} mods from mc-assets/mods/"
    echo ""
    read -p "Are you sure? Type 'DELETE' to confirm: " confirm

    if [ "$confirm" != "DELETE" ]; then
        echo "Cancelled"
        exit 0
    fi

    echo ""
    echo "Deleting mods..."
    rclone delete ${R2_REMOTE}:mc-assets/mods/ --progress

    print_success "All mods deleted!"
}

upload_mods() {
    check_rclone
    local MODS_FOLDER="$1"

    if [ -z "$MODS_FOLDER" ]; then
        print_error "Usage: ./r2-manage.sh upload-mods /path/to/mods/folder"
        exit 1
    fi

    if [ ! -d "$MODS_FOLDER" ]; then
        print_error "Directory '$MODS_FOLDER' does not exist"
        exit 1
    fi

    print_header "Upload Mods to R2"

    # Create temp directory for server mods only
    local TEMP_DIR=$(mktemp -d)
    trap "rm -rf $TEMP_DIR" EXIT

    local TOTAL=0
    local SKIPPED=0
    local COPIED=0

    echo "Filtering mods (removing client-only)..."

    for jar in "$MODS_FOLDER"/*.jar; do
        [ -f "$jar" ] || continue
        filename=$(basename "$jar")
        ((TOTAL++))

        # Check if mod should be skipped
        if [ -f "$SKIP_LIST" ] && grep -qxF "$filename" <(grep -v '^#' "$SKIP_LIST" | grep -v '^$' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'); then
            echo -e "  ${YELLOW}⏭ Skip:${NC} $filename (client-only)"
            ((SKIPPED++))
        else
            cp "$jar" "$TEMP_DIR/"
            ((COPIED++))
        fi
    done

    echo ""
    echo "Found $TOTAL total mods"
    echo "Skipping $SKIPPED client-only mods"
    echo "Uploading $COPIED server mods"
    echo ""

    read -p "Continue with upload? (y/n): " confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        echo "Cancelled"
        exit 0
    fi

    echo ""
    echo "Uploading to R2..."
    rclone copy "$TEMP_DIR/" ${R2_REMOTE}:mc-assets/mods/ --progress

    print_success "Upload complete! $COPIED mods uploaded"
}

list_world() {
    check_rclone
    print_header "World Data in R2"

    echo "Current world (mc-worlds/current/):"
    rclone ls ${R2_REMOTE}:mc-worlds/current/ 2>/dev/null | while read size name; do
        echo "  ${name} ($(numfmt --to=iec-i --suffix=B ${size} 2>/dev/null || echo "${size} B"))"
    done

    echo ""
    echo "Backups (mc-worlds/backups/):"
    rclone ls ${R2_REMOTE}:mc-worlds/backups/ 2>/dev/null | head -20 | while read size name; do
        echo "  ${name}"
    done

    local backup_count=$(rclone ls ${R2_REMOTE}:mc-worlds/backups/ 2>/dev/null | wc -l | tr -d ' ')
    if [ "$backup_count" -gt 20 ]; then
        echo "  ... and $((backup_count - 20)) more"
    fi
}

delete_world() {
    check_rclone
    print_header "Delete World Data from R2"

    echo "This will delete:"
    echo "  - Current world (mc-worlds/current/)"
    echo ""

    local size=$(rclone size ${R2_REMOTE}:mc-worlds/current/ 2>/dev/null | grep "Total size:" | cut -d: -f2 || echo "unknown")
    echo "Current world size: $size"
    echo ""

    print_warning "WARNING: This will reset the world to a fresh start!"
    print_warning "Make sure the server is OFFLINE before doing this!"
    echo ""

    read -p "Type 'RESET WORLD' to confirm: " confirm

    if [ "$confirm" != "RESET WORLD" ]; then
        echo "Cancelled"
        exit 0
    fi

    echo ""
    echo "Deleting world data..."
    rclone delete ${R2_REMOTE}:mc-worlds/current/ --progress

    print_success "World data deleted!"
    echo ""
    echo "Next server start will generate a fresh world."
}

# Help message
show_help() {
    echo "GroveMC R2 Management Script"
    echo ""
    echo "Usage: ./r2-manage.sh <command> [options]"
    echo ""
    echo "Commands:"
    echo "  setup              Configure rclone for R2 access"
    echo "  list-mods          List all mods currently in R2"
    echo "  delete-all-mods    Delete ALL mods from R2 (for fresh modpack)"
    echo "  upload-mods <path> Upload mods from local folder to R2"
    echo "  list-world         List world files in R2"
    echo "  delete-world       Delete world data (reset to fresh world)"
    echo "  help               Show this help message"
    echo ""
    echo "Examples:"
    echo "  # Fresh modpack workflow:"
    echo "  ./r2-manage.sh delete-all-mods"
    echo "  ./r2-manage.sh upload-mods /path/to/new/mods"
    echo ""
    echo "  # World reset workflow:"
    echo "  ./r2-manage.sh delete-world"
    echo ""
}

# Main command router
case "${1:-help}" in
    setup)
        setup_rclone
        ;;
    list-mods)
        list_mods
        ;;
    delete-all-mods)
        delete_all_mods
        ;;
    upload-mods)
        upload_mods "$2"
        ;;
    list-world)
        list_world
        ;;
    delete-world)
        delete_world
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        print_error "Unknown command: $1"
        show_help
        exit 1
        ;;
esac
