#!/bin/bash
# Upload server-required JAR files from a folder to R2 mc-assets/mods/
# Automatically skips client-only mods (shaders, UI, sound, etc.)
# Usage: ./upload-mods.sh /path/to/mods/folder

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKIP_LIST="$SCRIPT_DIR/client-only-mods.txt"

if [ -z "$1" ]; then
    echo "Usage: ./upload-mods.sh /path/to/mods/folder"
    echo "Example: ./upload-mods.sh ~/minecraft-server/mods"
    exit 1
fi

MODS_FOLDER="$1"

if [ ! -d "$MODS_FOLDER" ]; then
    echo "Error: Directory '$MODS_FOLDER' does not exist"
    exit 1
fi

# Check if skip list exists
if [ ! -f "$SKIP_LIST" ]; then
    echo "Warning: Skip list not found at $SKIP_LIST"
    echo "All mods will be uploaded. Continue? (y/n)"
    read -r response
    if [[ ! "$response" =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Function to check if a mod should be skipped
should_skip() {
    local filename="$1"
    if [ -f "$SKIP_LIST" ]; then
        # Search for exact filename match (ignoring comments and empty lines)
        grep -qxF "$filename" <(grep -v '^#' "$SKIP_LIST" | grep -v '^$' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')
        return $?
    fi
    return 1
}

# Count jars
JAR_COUNT=$(find "$MODS_FOLDER" -maxdepth 1 -name "*.jar" | wc -l | tr -d ' ')
SKIP_COUNT=$(grep -v '^#' "$SKIP_LIST" 2>/dev/null | grep -v '^$' | wc -l | tr -d ' ')

if [ "$JAR_COUNT" -eq 0 ]; then
    echo "No .jar files found in $MODS_FOLDER"
    exit 1
fi

echo "Found $JAR_COUNT total JAR files"
echo "Skip list has $SKIP_COUNT client-only mods"
echo "Uploading server-required mods to R2 bucket: mc-assets/mods/"
echo "---"

UPLOADED=0
SKIPPED=0
FAILED=0

for jar in "$MODS_FOLDER"/*.jar; do
    filename=$(basename "$jar")

    # Check if mod is in skip list
    if should_skip "$filename"; then
        echo "⏭ Skipping (client-only): $filename"
        ((SKIPPED++))
        continue
    fi

    echo -n "Uploading $filename... "

    if wrangler r2 object put "mc-assets/mods/$filename" --file="$jar" --remote 2>/dev/null; then
        echo "✓"
        ((UPLOADED++))
    else
        echo "✗ FAILED"
        ((FAILED++))
    fi
done

echo "---"
echo "Done!"
echo "  Uploaded: $UPLOADED"
echo "  Skipped (client-only): $SKIPPED"
echo "  Failed: $FAILED"
