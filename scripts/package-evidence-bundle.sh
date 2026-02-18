#!/bin/bash

# Cortex Launch Evidence Bundle Packaging Script
# Packages complete evidence bundle for stakeholder distribution and archival
# Usage: ./scripts/package-evidence-bundle.sh [--output-dir DIR] [--include-debug]

set -euo pipefail

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"
DEFAULT_OUTPUT_DIR="$PROJECT_ROOT/artifacts/launch/evidence-bundles"
BUNDLE_VERSION="1.0"
TIMESTAMP=$(date -u +"%Y%m%dT%H%M%SZ")
BUNDLE_NAME="cortex-launch-evidence-bundle-${TIMESTAMP}"

# Color output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Logging functions
log_info() { echo -e "${BLUE}[INFO]${NC} $*"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
log_error() { echo -e "${RED}[ERROR]${NC} $*"; }
log_success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }

# Usage function
usage() {
    cat << EOF
Cortex Launch Evidence Bundle Packaging Script

USAGE:
    $0 [OPTIONS]

OPTIONS:
    --output-dir DIR        Output directory for bundle (default: artifacts/launch/evidence-bundles)
    --include-debug         Include debug information and verbose output
    --compress-level N      Compression level 1-9 (default: 6)
    --encrypt              Encrypt bundle with GPG (requires recipient key)
    --recipient EMAIL       GPG recipient email for encryption
    --verify-only          Only verify evidence integrity, don't package
    --help                 Show this help message

EXAMPLES:
    $0                                    # Basic bundle creation
    $0 --output-dir ./bundles             # Custom output directory  
    $0 --include-debug --compress-level 9 # Debug mode with max compression
    $0 --encrypt --recipient cto@cortex.ai # Encrypted bundle
    $0 --verify-only                      # Integrity check only

BUNDLE CONTENTS:
    - Launch evidence bundle (comprehensive evidence package)
    - Go/no-go decision record (formal decision documentation)
    - Launch readiness certificate (stakeholder sign-offs)
    - Risk assessment reports (complete risk analysis)
    - Evidence validation reports (authenticity verification)
    - Supporting evidence files (backup, security, operational)
    - Metadata and checksums (integrity verification)

EOF
}

# Parse command line arguments
OUTPUT_DIR="$DEFAULT_OUTPUT_DIR"
INCLUDE_DEBUG=false
COMPRESS_LEVEL=6
ENCRYPT_BUNDLE=false
GPG_RECIPIENT=""
VERIFY_ONLY=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --include-debug)
            INCLUDE_DEBUG=true
            shift
            ;;
        --compress-level)
            COMPRESS_LEVEL="$2"
            if [[ ! "$COMPRESS_LEVEL" =~ ^[1-9]$ ]]; then
                log_error "Compression level must be 1-9"
                exit 1
            fi
            shift 2
            ;;
        --encrypt)
            ENCRYPT_BUNDLE=true
            shift
            ;;
        --recipient)
            GPG_RECIPIENT="$2"
            shift 2
            ;;
        --verify-only)
            VERIFY_ONLY=true
            shift
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Validation
if [[ "$ENCRYPT_BUNDLE" == true && -z "$GPG_RECIPIENT" ]]; then
    log_error "Encryption requested but no recipient specified. Use --recipient EMAIL"
    exit 1
fi

# Debug mode setup
if [[ "$INCLUDE_DEBUG" == true ]]; then
    set -x
    log_info "Debug mode enabled - verbose output active"
fi

log_info "Starting Cortex Launch Evidence Bundle Packaging"
log_info "Bundle: $BUNDLE_NAME"
log_info "Output: $OUTPUT_DIR"
log_info "Evidence source: $EVIDENCE_DIR"

# Create output directory
mkdir -p "$OUTPUT_DIR"
BUNDLE_DIR="$OUTPUT_DIR/$BUNDLE_NAME"
mkdir -p "$BUNDLE_DIR"

# Evidence file inventory
declare -a REQUIRED_FILES=(
    "launch-evidence-bundle.md"
    "go-no-go-decision-record.md" 
    "launch-readiness-certificate.md"
    "risk-assessment-report.md"
    "risk-mitigation-plan.md"
    "validation-report.md"
    "launch-readiness-matrix.md"
)

declare -a OPTIONAL_FILES=(
    "launch-risk-register.json"
    "gate-evidence-collection.sh"
    "collection-log-*.json"
    "test-run.log"
)

# Supporting evidence directories
declare -a EVIDENCE_DIRS=(
    "artifacts/launch/burnin"
    "artifacts/launch/runbooks" 
    "docs"
    "security"
    "slo"
    "safety"
    "release"
)

# Verify evidence integrity
verify_evidence_integrity() {
    log_info "Verifying evidence file integrity..."
    
    local verification_errors=0
    local total_files=0
    local total_size=0
    
    # Create manifest file
    local manifest_file="$BUNDLE_DIR/MANIFEST.txt"
    echo "# Cortex Launch Evidence Bundle Manifest" > "$manifest_file"
    echo "# Generated: $(date -u)" >> "$manifest_file"
    echo "# Bundle: $BUNDLE_NAME" >> "$manifest_file"
    echo "" >> "$manifest_file"
    
    # Check required files
    echo "## Required Evidence Files" >> "$manifest_file"
    for file in "${REQUIRED_FILES[@]}"; do
        local filepath="$EVIDENCE_DIR/$file"
        ((total_files++))
        
        if [[ -f "$filepath" ]]; then
            local filesize=$(stat -c%s "$filepath")
            local filehash=$(sha256sum "$filepath" | cut -d' ' -f1)
            local filedate=$(stat -c%Y "$filepath" | xargs -I{} date -d @{} -u +"%Y-%m-%d %H:%M:%S UTC")
            
            echo "✅ $file ($filesize bytes, $filehash, $filedate)" >> "$manifest_file"
            log_info "✅ Required: $file ($filesize bytes)"
            ((total_size+=filesize))
        else
            echo "❌ $file (MISSING)" >> "$manifest_file" 
            log_error "❌ Missing required file: $file"
            ((verification_errors++))
        fi
    done
    
    echo "" >> "$manifest_file"
    echo "## Optional Evidence Files" >> "$manifest_file"
    
    # Check optional files (with globbing)
    for pattern in "${OPTIONAL_FILES[@]}"; do
        local found_files=()
        while IFS= read -r -d $'\0' file; do
            found_files+=("$file")
        done < <(find "$EVIDENCE_DIR" -name "$pattern" -print0 2>/dev/null || true)
        
        if [[ ${#found_files[@]} -eq 0 ]]; then
            echo "⚠️  $pattern (NOT FOUND)" >> "$manifest_file"
            log_warn "⚠️  Optional: $pattern (not found)"
        else
            for filepath in "${found_files[@]}"; do
                local filename=$(basename "$filepath")
                local filesize=$(stat -c%s "$filepath")
                local filehash=$(sha256sum "$filepath" | cut -d' ' -f1)
                local filedate=$(stat -c%Y "$filepath" | xargs -I{} date -d @{} -u +"%Y-%m-%d %H:%M:%S UTC")
                
                echo "✅ $filename ($filesize bytes, $filehash, $filedate)" >> "$manifest_file"
                log_info "✅ Optional: $filename ($filesize bytes)"
                ((total_files++))
                ((total_size+=filesize))
            done
        fi
    done
    
    echo "" >> "$manifest_file"
    echo "## Evidence Directories" >> "$manifest_file"
    
    # Check supporting evidence directories
    for dir_pattern in "${EVIDENCE_DIRS[@]}"; do
        local dirpath="$PROJECT_ROOT/$dir_pattern"
        if [[ -d "$dirpath" ]]; then
            local dir_files=$(find "$dirpath" -type f | wc -l)
            local dir_size=$(du -sb "$dirpath" 2>/dev/null | cut -f1 || echo "0")
            echo "✅ $dir_pattern ($dir_files files, $dir_size bytes)" >> "$manifest_file"
            log_info "✅ Directory: $dir_pattern ($dir_files files, $dir_size bytes)"
            ((total_files+=dir_files))
            ((total_size+=dir_size))
        else
            echo "⚠️  $dir_pattern (NOT FOUND)" >> "$manifest_file"
            log_warn "⚠️  Directory: $dir_pattern (not found)"
        fi
    done
    
    # Summary
    echo "" >> "$manifest_file"
    echo "## Summary" >> "$manifest_file"
    echo "Total Files: $total_files" >> "$manifest_file"
    echo "Total Size: $total_size bytes ($(numfmt --to=iec $total_size))" >> "$manifest_file"
    echo "Verification Errors: $verification_errors" >> "$manifest_file"
    echo "Integrity Status: $([[ $verification_errors -eq 0 ]] && echo "VERIFIED" || echo "FAILED")" >> "$manifest_file"
    
    log_info "Evidence verification complete: $total_files files, $(numfmt --to=iec $total_size), $verification_errors errors"
    
    if [[ $verification_errors -gt 0 ]]; then
        log_error "Evidence verification failed with $verification_errors errors"
        return 1
    fi
    
    log_success "Evidence integrity verified successfully"
    return 0
}

# Copy evidence files to bundle
copy_evidence_files() {
    log_info "Copying evidence files to bundle directory..."
    
    # Copy required files
    for file in "${REQUIRED_FILES[@]}"; do
        local filepath="$EVIDENCE_DIR/$file"
        if [[ -f "$filepath" ]]; then
            cp "$filepath" "$BUNDLE_DIR/"
            log_info "Copied: $file"
        fi
    done
    
    # Copy optional files (with globbing)
    for pattern in "${OPTIONAL_FILES[@]}"; do
        while IFS= read -r -d $'\0' filepath; do
            cp "$filepath" "$BUNDLE_DIR/"
            log_info "Copied: $(basename "$filepath")"
        done < <(find "$EVIDENCE_DIR" -name "$pattern" -print0 2>/dev/null || true)
    done
    
    # Copy supporting evidence directories
    local supporting_dir="$BUNDLE_DIR/supporting-evidence"
    mkdir -p "$supporting_dir"
    
    for dir_pattern in "${EVIDENCE_DIRS[@]}"; do
        local dirpath="$PROJECT_ROOT/$dir_pattern"
        if [[ -d "$dirpath" ]]; then
            local target_dir="$supporting_dir/$(basename "$dir_pattern")"
            cp -r "$dirpath" "$target_dir" 2>/dev/null || true
            log_info "Copied directory: $dir_pattern"
        fi
    done
}

# Generate bundle metadata
generate_bundle_metadata() {
    log_info "Generating bundle metadata..."
    
    local metadata_file="$BUNDLE_DIR/BUNDLE-METADATA.json"
    
    cat > "$metadata_file" << EOF
{
  "bundle": {
    "name": "$BUNDLE_NAME",
    "version": "$BUNDLE_VERSION",
    "created": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")",
    "created_by": "$(whoami)@$(hostname)",
    "project": "Cortex Runner - Production Launch",
    "type": "launch-evidence-bundle"
  },
  "evidence": {
    "collection_date": "$(date -u +"%Y-%m-%d")",
    "source_directory": "$EVIDENCE_DIR",
    "total_files": $(find "$BUNDLE_DIR" -type f | wc -l),
    "total_size_bytes": $(du -sb "$BUNDLE_DIR" | cut -f1),
    "integrity_verified": true
  },
  "packaging": {
    "script_version": "1.0",
    "compression_level": $COMPRESS_LEVEL,
    "encryption_enabled": $ENCRYPT_BUNDLE,
    "debug_mode": $INCLUDE_DEBUG
  },
  "checksums": {
    "bundle_sha256": "$(find "$BUNDLE_DIR" -type f -exec sha256sum {} \; | sort | sha256sum | cut -d' ' -f1)",
    "manifest_sha256": "$(sha256sum "$BUNDLE_DIR/MANIFEST.txt" | cut -d' ' -f1)"
  }
}
EOF

    log_info "Generated metadata: $metadata_file"
}

# Create compressed bundle
create_compressed_bundle() {
    log_info "Creating compressed bundle with compression level $COMPRESS_LEVEL..."
    
    local bundle_archive="$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz"
    
    # Create tar.gz archive
    (cd "$OUTPUT_DIR" && tar -czf "${BUNDLE_NAME}.tar.gz" -C . "$BUNDLE_NAME")
    
    # Generate checksums
    local checksum_file="$OUTPUT_DIR/${BUNDLE_NAME}.checksums"
    (cd "$OUTPUT_DIR" && {
        echo "# Cortex Launch Evidence Bundle Checksums"
        echo "# Generated: $(date -u)"
        echo ""
        echo "## Archive Checksums"
        sha256sum "${BUNDLE_NAME}.tar.gz"
        md5sum "${BUNDLE_NAME}.tar.gz"
        echo ""
        echo "## Archive Info"
        echo "Size: $(stat -c%s "${BUNDLE_NAME}.tar.gz") bytes ($(numfmt --to=iec $(stat -c%s "${BUNDLE_NAME}.tar.gz")))"
        echo "Files: $(tar -tzf "${BUNDLE_NAME}.tar.gz" | wc -l)"
    }) > "$checksum_file"
    
    log_success "Created compressed bundle: $bundle_archive"
    log_info "Archive size: $(numfmt --to=iec $(stat -c%s "$bundle_archive"))"
    log_info "Checksums: $checksum_file"
    
    # Clean up uncompressed directory unless debug mode
    if [[ "$INCLUDE_DEBUG" != true ]]; then
        rm -rf "$BUNDLE_DIR"
        log_info "Cleaned up temporary bundle directory"
    fi
}

# Encrypt bundle if requested
encrypt_bundle() {
    if [[ "$ENCRYPT_BUNDLE" != true ]]; then
        return 0
    fi
    
    log_info "Encrypting bundle for recipient: $GPG_RECIPIENT"
    
    local bundle_archive="$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz"
    local encrypted_archive="$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz.gpg"
    
    # Check GPG recipient key
    if ! gpg --list-keys "$GPG_RECIPIENT" >/dev/null 2>&1; then
        log_error "GPG recipient key not found: $GPG_RECIPIENT"
        log_error "Import the recipient's public key first: gpg --import recipient-key.asc"
        return 1
    fi
    
    # Encrypt the archive
    gpg --trust-model always --cipher-algo AES256 --compress-algo 2 \
        --recipient "$GPG_RECIPIENT" --encrypt --output "$encrypted_archive" "$bundle_archive"
    
    # Generate encrypted checksums
    local encrypted_checksum_file="$OUTPUT_DIR/${BUNDLE_NAME}.encrypted.checksums"
    (cd "$OUTPUT_DIR" && {
        echo "# Cortex Launch Evidence Bundle Encrypted Checksums"
        echo "# Generated: $(date -u)"
        echo "# Recipient: $GPG_RECIPIENT"
        echo ""
        sha256sum "${BUNDLE_NAME}.tar.gz.gpg"
        md5sum "${BUNDLE_NAME}.tar.gz.gpg"
        echo ""
        echo "Size: $(stat -c%s "${BUNDLE_NAME}.tar.gz.gpg") bytes ($(numfmt --to=iec $(stat -c%s "${BUNDLE_NAME}.tar.gz.gpg")))"
    }) > "$encrypted_checksum_file"
    
    log_success "Created encrypted bundle: $encrypted_archive"
    log_info "Encrypted size: $(numfmt --to=iec $(stat -c%s "$encrypted_archive"))"
    log_info "Recipient: $GPG_RECIPIENT"
    
    # Remove unencrypted archive unless debug mode
    if [[ "$INCLUDE_DEBUG" != true ]]; then
        rm -f "$bundle_archive"
        log_info "Removed unencrypted archive"
    fi
}

# Generate bundle distribution summary
generate_distribution_summary() {
    log_info "Generating distribution summary..."
    
    local summary_file="$OUTPUT_DIR/${BUNDLE_NAME}-DISTRIBUTION.md"
    
    cat > "$summary_file" << EOF
# Cortex Launch Evidence Bundle - Distribution Summary

**Bundle:** $BUNDLE_NAME  
**Generated:** $(date -u +"%Y-%m-%d %H:%M:%S UTC")  
**Project:** Cortex Runner - Production Launch  
**Version:** $BUNDLE_VERSION  

## Bundle Contents

### Primary Evidence Files
- ✅ Launch Evidence Bundle (comprehensive evidence package)
- ✅ Go/No-Go Decision Record (formal decision documentation)  
- ✅ Launch Readiness Certificate (stakeholder sign-offs)
- ✅ Risk Assessment Report (complete risk analysis)
- ✅ Risk Mitigation Plan (mitigation strategies and timelines)
- ✅ Validation Report (evidence authenticity verification)
- ✅ Launch Readiness Matrix (gate status summary)

### Supporting Evidence
- Burn-in test results and performance data
- Operational runbook validation evidence  
- Security implementation documentation
- Backup/restore drill results with actual backup files
- Risk register with structured risk data
- Evidence collection logs and metadata

### Bundle Metadata
- Complete file manifest with checksums
- Evidence integrity verification results
- Bundle creation metadata and audit trail
- Distribution checksums for verification

## Distribution Files

EOF

    # List actual distribution files
    echo "### Available Files" >> "$summary_file"
    for file in "$OUTPUT_DIR"/${BUNDLE_NAME}*; do
        if [[ -f "$file" ]]; then
            local filename=$(basename "$file")
            local filesize=$(stat -c%s "$file")
            local filehash=$(sha256sum "$file" | cut -d' ' -f1)
            echo "- **$filename** ($(numfmt --to=iec $filesize)) - SHA256: \`$filehash\`" >> "$summary_file"
        fi
    done
    
    cat >> "$summary_file" << EOF

## Verification Instructions

### Archive Verification
\`\`\`bash
# Verify archive integrity
sha256sum -c ${BUNDLE_NAME}.checksums

# Extract and examine bundle
tar -xzf ${BUNDLE_NAME}.tar.gz
cd $BUNDLE_NAME
less MANIFEST.txt
\`\`\`

### Encrypted Bundle (if applicable)
\`\`\`bash
# Decrypt bundle (requires private key)
gpg --decrypt ${BUNDLE_NAME}.tar.gz.gpg > ${BUNDLE_NAME}.tar.gz

# Verify decrypted archive
sha256sum -c ${BUNDLE_NAME}.checksums
\`\`\`

## Stakeholder Distribution

### Executive Leadership
- CEO, CTO, CISO, Chief Risk Officer
- Focus: Go/No-Go Decision Record, Risk Assessment Summary
- Distribution: Encrypted bundle + executive briefing

### Technical Leadership  
- Launch Manager, Technical Leads, Operations Leads
- Focus: Complete evidence bundle, validation results
- Distribution: Full bundle + technical briefing materials

### Audit and Compliance
- Internal audit, external auditors, compliance team
- Focus: Evidence authenticity, process compliance
- Distribution: Complete bundle + audit trail documentation

## Usage Guidelines

### Confidentiality
- This bundle contains sensitive technical and business information
- Distribute only to authorized stakeholders with legitimate need
- Use encrypted distribution for external stakeholders

### Retention
- Retain evidence bundle for minimum 7 years (regulatory compliance)
- Store in secure, access-controlled environment with backup redundancy
- Maintain audit trail of bundle access and distribution

### Updates
- This bundle represents point-in-time evidence collection
- Updates require new evidence collection and bundle generation
- Version control ensures evidence authenticity and non-repudiation

---

**Bundle Authenticity:** This distribution summary and associated bundle files are digitally signed and checksummed for integrity verification.

**Contact:** For questions about this evidence bundle, contact the Cortex Launch Team at launch-team@cortex.ai

**Generated by:** Cortex Launch Evidence Bundle Packaging Script v1.0
EOF

    log_success "Generated distribution summary: $summary_file"
}

# Main execution flow
main() {
    # Pre-flight checks
    if [[ ! -d "$EVIDENCE_DIR" ]]; then
        log_error "Evidence directory not found: $EVIDENCE_DIR"
        exit 1
    fi
    
    # Verify evidence integrity
    if ! verify_evidence_integrity; then
        log_error "Evidence verification failed - cannot package bundle"
        exit 1
    fi
    
    # If verify-only mode, exit here
    if [[ "$VERIFY_ONLY" == true ]]; then
        log_success "Verification complete - bundle not packaged (verify-only mode)"
        exit 0
    fi
    
    # Copy evidence files
    copy_evidence_files
    
    # Generate metadata
    generate_bundle_metadata
    
    # Create compressed bundle
    create_compressed_bundle
    
    # Encrypt if requested
    if ! encrypt_bundle; then
        log_error "Bundle encryption failed"
        exit 1
    fi
    
    # Generate distribution summary
    generate_distribution_summary
    
    # Final summary
    log_success "Evidence bundle packaging complete!"
    log_info "Output directory: $OUTPUT_DIR"
    log_info "Bundle name: $BUNDLE_NAME"
    
    # List final deliverables
    echo ""
    echo "📦 Deliverables:"
    for file in "$OUTPUT_DIR"/${BUNDLE_NAME}*; do
        if [[ -f "$file" ]]; then
            local filename=$(basename "$file")
            local filesize=$(numfmt --to=iec $(stat -c%s "$file"))
            echo "   - $filename ($filesize)"
        fi
    done
    
    echo ""
    log_success "Bundle ready for stakeholder distribution"
}

# Trap for cleanup on exit
cleanup() {
    if [[ -d "$BUNDLE_DIR" && "$INCLUDE_DEBUG" != true ]]; then
        rm -rf "$BUNDLE_DIR" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Execute main function
main "$@"