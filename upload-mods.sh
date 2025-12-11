#!/bin/bash
# Upload all JAR files from a folder to R2 mc-assets/mods/
# Usage: ./upload-mods.sh /path/to/mods/folder

set -e

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

# Count jars
JAR_COUNT=$(find "$MODS_FOLDER" -maxdepth 1 -name "*.jar" | wc -l | tr -d ' ')

if [ "$JAR_COUNT" -eq 0 ]; then
    echo "No .jar files found in $MODS_FOLDER"
    exit 1
fi

echo "Found $JAR_COUNT JAR files to upload"
echo "Uploading to R2 bucket: mc-assets/mods/"
echo "---"

UPLOADED=0
FAILED=0

for jar in "$MODS_FOLDER"/*.jar; do
    filename=$(basename "$jar")
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
echo "Done! Uploaded: $UPLOADED, Failed: $FAILED"
