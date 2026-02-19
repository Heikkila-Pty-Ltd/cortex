#!/bin/bash

# Cortex Launch Evidence Bundle Packaging Script
# 
# This script packages all launch evidence, assessments, and decision records
# into a comprehensive bundle for distribution and archival.
#
# Usage: ./package-evidence-bundle.sh [options]
# Options:
#   --output-dir PATH    Specify output directory (default: ./launch-evidence-packages)
#   --bundle-name NAME   Specify bundle name (default: cortex-launch-evidence-TIMESTAMP)
#   --format FORMAT      Package format: tar.gz, zip, or both (default: both)
#   --include-logs       Include detailed collection logs (default: false)
#   --verify            Verify bundle integrity after creation (default: true)
#   --distribute        Copy bundle to distribution locations (default: false)
#   --help              Show this help message

set -euo pipefail

# Script configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
EVIDENCE_DIR="$PROJECT_ROOT/evidence"
TIMESTAMP=$(date -u +%Y%m%d-%H%M%S)
DEFAULT_BUNDLE_NAME="cortex-launch-evidence-$TIMESTAMP"

# Default options
OUTPUT_DIR="$PROJECT_ROOT/launch-evidence-packages"
BUNDLE_NAME="$DEFAULT_BUNDLE_NAME"
PACKAGE_FORMAT="both"  # tar.gz, zip, or both
INCLUDE_LOGS=false
VERIFY_BUNDLE=true
DISTRIBUTE=false

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Print functions
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Help function
show_help() {
    cat << EOF
Cortex Launch Evidence Bundle Packaging Script

Usage: $0 [options]

Options:
  --output-dir PATH     Specify output directory (default: ./launch-evidence-packages)
  --bundle-name NAME    Specify bundle name (default: cortex-launch-evidence-TIMESTAMP)
  --format FORMAT       Package format: tar.gz, zip, or both (default: both)
  --include-logs        Include detailed collection logs (default: false)
  --verify             Verify bundle integrity after creation (default: true)
  --distribute         Copy bundle to distribution locations (default: false)
  --help               Show this help message

Examples:
  $0                                          # Create bundle with default options
  $0 --format zip --include-logs              # Create ZIP bundle with logs
  $0 --bundle-name final-launch-evidence      # Custom bundle name
  $0 --distribute                             # Create and distribute bundle

Bundle Contents:
  - Launch evidence bundle (comprehensive summary)
  - Go/No-Go decision record (formal decision documentation)
  - Launch readiness certificate (stakeholder sign-offs)
  - Risk assessment and mitigation plans
  - Launch readiness matrix and validation reports
  - Evidence collection metadata and manifests
  - Optional: Detailed collection logs

Distribution Locations:
  - Stakeholder review directories
  - Compliance archive locations  
  - Executive leadership folders
  - Engineering team workspaces

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --bundle-name)
            BUNDLE_NAME="$2"
            shift 2
            ;;
        --format)
            PACKAGE_FORMAT="$2"
            if [[ ! "$PACKAGE_FORMAT" =~ ^(tar\.gz|zip|both)$ ]]; then
                print_error "Invalid format: $PACKAGE_FORMAT. Must be tar.gz, zip, or both"
                exit 1
            fi
            shift 2
            ;;
        --include-logs)
            INCLUDE_LOGS=true
            shift
            ;;
        --verify)
            VERIFY_BUNDLE=true
            shift
            ;;
        --distribute)
            DISTRIBUTE=true
            shift
            ;;
        --help)
            show_help
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
done

# Validate environment
validate_environment() {
    print_info "Validating environment..."
    
    if [[ ! -d "$EVIDENCE_DIR" ]]; then
        print_error "Evidence directory not found: $EVIDENCE_DIR"
        exit 1
    fi
    
    if [[ ! -f "$EVIDENCE_DIR/launch-evidence-bundle.md" ]]; then
        print_error "Launch evidence bundle not found: $EVIDENCE_DIR/launch-evidence-bundle.md"
        exit 1
    fi
    
    if [[ ! -f "$EVIDENCE_DIR/go-no-go-decision-record.md" ]]; then
        print_error "Go/No-Go decision record not found: $EVIDENCE_DIR/go-no-go-decision-record.md"
        exit 1
    fi
    
    if [[ ! -f "$EVIDENCE_DIR/launch-readiness-certificate.md" ]]; then
        print_error "Launch readiness certificate not found: $EVIDENCE_DIR/launch-readiness-certificate.md"
        exit 1
    fi
    
    # Check required tools
    local missing_tools=()
    command -v tar >/dev/null 2>&1 || missing_tools+=("tar")
    if [[ "$PACKAGE_FORMAT" == "zip" ]] || [[ "$PACKAGE_FORMAT" == "both" ]]; then
        command -v zip >/dev/null 2>&1 || missing_tools+=("zip")
    fi
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        print_error "Missing required tools: ${missing_tools[*]}"
        exit 1
    fi
    
    print_success "Environment validation complete"
}

# Create output directory
create_output_dir() {
    print_info "Creating output directory: $OUTPUT_DIR"
    mkdir -p "$OUTPUT_DIR"
    
    if [[ ! -w "$OUTPUT_DIR" ]]; then
        print_error "Output directory not writable: $OUTPUT_DIR"
        exit 1
    fi
}

# Generate bundle manifest
generate_manifest() {
    local bundle_dir="$1"
    local manifest_file="$bundle_dir/BUNDLE_MANIFEST.md"
    
    print_info "Generating bundle manifest..."
    
    cat > "$manifest_file" << EOF
# Cortex Launch Evidence Bundle Manifest

**Bundle Name:** $BUNDLE_NAME  
**Bundle Date:** $(date -u +%Y-%m-%dT%H:%M:%SZ)  
**Bundle Version:** 1.0  
**System:** Cortex Autonomous Agent Orchestrator v1.0.0  
**Evidence Period:** 2026-02-12 to 2026-02-18  

## Bundle Contents

### Primary Documents
- \`launch-evidence-bundle.md\` - Comprehensive evidence package and analysis
- \`go-no-go-decision-record.md\` - Formal launch decision with rationale
- \`launch-readiness-certificate.md\` - Stakeholder approval and compliance certification

### Assessment Reports
- \`risk-assessment-report.md\` - Comprehensive risk analysis and evaluation
- \`risk-mitigation-plan.md\` - Risk mitigation strategies and implementation
- \`launch-readiness-matrix.md\` - Gate-by-gate evidence status matrix
- \`validation-report.md\` - System validation and testing results

### Supporting Evidence
- \`launch-risk-register.json\` - Detailed risk register with metadata
EOF

    if [[ "$INCLUDE_LOGS" == "true" ]]; then
        cat >> "$manifest_file" << EOF
- \`collection-logs/\` - Detailed evidence collection execution logs
EOF
    fi

    cat >> "$manifest_file" << EOF

### Metadata
- \`BUNDLE_MANIFEST.md\` - This manifest file
- \`BUNDLE_CHECKSUM.txt\` - File integrity checksums
- \`BUNDLE_README.txt\` - Bundle usage and distribution instructions

## File Integrity

All files in this bundle have been validated for integrity. See \`BUNDLE_CHECKSUM.txt\` for individual file checksums.

## Distribution

This bundle is intended for distribution to:
- Launch Review Board members
- Executive leadership team
- Engineering and operations teams
- Compliance and audit teams
- External stakeholders as required

## Usage

1. **Review Process:** Start with \`launch-evidence-bundle.md\` for comprehensive overview
2. **Decision Analysis:** Review \`go-no-go-decision-record.md\` for formal decision rationale
3. **Compliance Check:** Reference \`launch-readiness-certificate.md\` for stakeholder approvals
4. **Risk Assessment:** Consult risk assessment and mitigation documents for detailed analysis

## Archive Information

- **Bundle Format:** Multiple formats available (tar.gz, zip)
- **Retention Period:** 7 years from system deployment
- **Archive Location:** Secure document management system
- **Access Control:** Role-based access with audit logging

---

*This manifest was generated automatically by the evidence bundle packaging system.*
EOF

    print_success "Bundle manifest generated: $manifest_file"
}

# Generate checksums
generate_checksums() {
    local bundle_dir="$1"
    local checksum_file="$bundle_dir/BUNDLE_CHECKSUM.txt"
    
    print_info "Generating file checksums..."
    
    (
        cd "$bundle_dir"
        find . -type f -not -name "BUNDLE_CHECKSUM.txt" -exec sha256sum {} \; | sort > BUNDLE_CHECKSUM.txt
    )
    
    print_success "Checksums generated: $checksum_file"
}

# Create bundle README
create_readme() {
    local bundle_dir="$1"
    local readme_file="$bundle_dir/BUNDLE_README.txt"
    
    print_info "Creating bundle README..."
    
    cat > "$readme_file" << EOF
CORTEX LAUNCH EVIDENCE BUNDLE
=============================

Bundle: $BUNDLE_NAME
Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)
System: Cortex Autonomous Agent Orchestrator v1.0.0

QUICK START
-----------
1. Start with 'launch-evidence-bundle.md' for comprehensive overview
2. Review 'go-no-go-decision-record.md' for formal launch decision
3. Check 'launch-readiness-certificate.md' for compliance status

BUNDLE STRUCTURE  
----------------
Primary Documents:
  launch-evidence-bundle.md      - Master evidence summary
  go-no-go-decision-record.md    - Formal launch decision
  launch-readiness-certificate.md - Compliance certification

Assessment Reports:
  risk-assessment-report.md      - Risk analysis
  risk-mitigation-plan.md        - Mitigation strategies  
  launch-readiness-matrix.md     - Evidence status matrix
  validation-report.md           - Testing results

Supporting Files:
  launch-risk-register.json      - Risk metadata
  BUNDLE_MANIFEST.md             - Complete manifest
  BUNDLE_CHECKSUM.txt            - Integrity checksums
  BUNDLE_README.txt              - This file

DECISION SUMMARY
----------------
Launch Decision: NO-GO
Reason: Critical reliability and safety gaps
Action Required: Complete P0 remediation work
Timeline: 21-33 days estimated

DISTRIBUTION
------------
This bundle should be distributed to all Launch Review Board members
and relevant stakeholders for review and action planning.

For questions about this bundle, contact:
- Launch Team Lead: launch-team@cortex.local
- Risk Assessment: risk-assessment@cortex.local
- Safety Review: safety@cortex.local

VERIFICATION  
------------
To verify bundle integrity:
  sha256sum -c BUNDLE_CHECKSUM.txt

All files should show "OK" status.
EOF

    print_success "Bundle README created: $readme_file"
}

# Copy evidence files
copy_evidence_files() {
    local bundle_dir="$1"
    
    print_info "Copying evidence files..."
    
    # Primary documents (required)
    local primary_files=(
        "launch-evidence-bundle.md"
        "go-no-go-decision-record.md" 
        "launch-readiness-certificate.md"
    )
    
    for file in "${primary_files[@]}"; do
        if [[ -f "$EVIDENCE_DIR/$file" ]]; then
            cp "$EVIDENCE_DIR/$file" "$bundle_dir/"
            print_success "Copied: $file"
        else
            print_error "Required file missing: $file"
            return 1
        fi
    done
    
    # Assessment reports (best effort)
    local assessment_files=(
        "risk-assessment-report.md"
        "risk-mitigation-plan.md"
        "launch-readiness-matrix.md"
        "validation-report.md"
        "launch-risk-register.json"
    )
    
    for file in "${assessment_files[@]}"; do
        if [[ -f "$EVIDENCE_DIR/$file" ]]; then
            cp "$EVIDENCE_DIR/$file" "$bundle_dir/"
            print_success "Copied: $file"
        else
            print_warning "Optional file missing: $file"
        fi
    done
    
    # Collection logs (if requested)
    if [[ "$INCLUDE_LOGS" == "true" ]]; then
        print_info "Including collection logs..."
        if ls "$EVIDENCE_DIR"/collection-log-*.json >/dev/null 2>&1; then
            mkdir -p "$bundle_dir/collection-logs"
            cp "$EVIDENCE_DIR"/collection-log-*.json "$bundle_dir/collection-logs/"
            print_success "Collection logs copied"
        else
            print_warning "No collection logs found"
        fi
    fi
}

# Create package archives
create_packages() {
    local bundle_dir="$1"
    local bundle_name="$2"
    
    print_info "Creating package archives..."
    
    cd "$OUTPUT_DIR"
    
    if [[ "$PACKAGE_FORMAT" == "tar.gz" ]] || [[ "$PACKAGE_FORMAT" == "both" ]]; then
        print_info "Creating tar.gz archive..."
        tar -czf "${bundle_name}.tar.gz" -C "$bundle_dir/.." "$(basename "$bundle_dir")"
        print_success "Created: ${bundle_name}.tar.gz"
    fi
    
    if [[ "$PACKAGE_FORMAT" == "zip" ]] || [[ "$PACKAGE_FORMAT" == "both" ]]; then
        print_info "Creating zip archive..."
        (cd "$bundle_dir/.." && zip -r "$OUTPUT_DIR/${bundle_name}.zip" "$(basename "$bundle_dir")")
        print_success "Created: ${bundle_name}.zip"
    fi
}

# Verify bundle integrity
verify_bundle() {
    local bundle_dir="$1"
    
    if [[ "$VERIFY_BUNDLE" != "true" ]]; then
        return 0
    fi
    
    print_info "Verifying bundle integrity..."
    
    (
        cd "$bundle_dir"
        if sha256sum -c BUNDLE_CHECKSUM.txt --quiet; then
            print_success "Bundle integrity verified"
        else
            print_error "Bundle integrity verification failed"
            return 1
        fi
    )
}

# Distribution function  
distribute_bundle() {
    if [[ "$DISTRIBUTE" != "true" ]]; then
        return 0
    fi
    
    print_info "Distributing bundle packages..."
    
    # Define distribution locations
    local dist_locations=(
        "/tmp/stakeholder-review"      # Stakeholder review directory
        "/tmp/compliance-archive"      # Compliance archive
        "/tmp/executive-briefing"      # Executive leadership
    )
    
    # Create distribution directories and copy files
    for location in "${dist_locations[@]}"; do
        if mkdir -p "$location" 2>/dev/null; then
            if [[ -f "$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz" ]]; then
                cp "$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz" "$location/"
                print_success "Distributed tar.gz to: $location"
            fi
            if [[ -f "$OUTPUT_DIR/${BUNDLE_NAME}.zip" ]]; then
                cp "$OUTPUT_DIR/${BUNDLE_NAME}.zip" "$location/"
                print_success "Distributed zip to: $location"
            fi
        else
            print_warning "Cannot access distribution location: $location"
        fi
    done
}

# Generate summary report
generate_summary() {
    local bundle_dir="$1"
    
    print_info "Bundle packaging completed successfully!"
    echo ""
    echo "=== PACKAGE SUMMARY ==="
    echo "Bundle Name: $BUNDLE_NAME"
    echo "Bundle Directory: $bundle_dir"
    echo "Output Directory: $OUTPUT_DIR" 
    echo "Package Format: $PACKAGE_FORMAT"
    echo "Include Logs: $INCLUDE_LOGS"
    echo "Distribution: $DISTRIBUTE"
    echo ""
    
    echo "=== FILES CREATED ==="
    if [[ -f "$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz" ]]; then
        echo "- ${BUNDLE_NAME}.tar.gz ($(du -h "$OUTPUT_DIR/${BUNDLE_NAME}.tar.gz" | cut -f1))"
    fi
    if [[ -f "$OUTPUT_DIR/${BUNDLE_NAME}.zip" ]]; then
        echo "- ${BUNDLE_NAME}.zip ($(du -h "$OUTPUT_DIR/${BUNDLE_NAME}.zip" | cut -f1))"
    fi
    echo "- Bundle directory: $(du -sh "$bundle_dir" | cut -f1)"
    echo ""
    
    echo "=== BUNDLE CONTENTS ==="
    echo "Primary Documents: 3 files"
    echo "Assessment Reports: $(find "$bundle_dir" -name "*.md" -o -name "*.json" | wc -l) files"
    if [[ "$INCLUDE_LOGS" == "true" ]]; then
        if [[ -d "$bundle_dir/collection-logs" ]]; then
            echo "Collection Logs: $(find "$bundle_dir/collection-logs" -name "*.json" | wc -l) files"
        fi
    fi
    echo ""
    
    echo "=== NEXT STEPS ==="
    echo "1. Verify bundle integrity: cd $bundle_dir && sha256sum -c BUNDLE_CHECKSUM.txt"
    echo "2. Review bundle contents: cat $bundle_dir/BUNDLE_README.txt"
    echo "3. Distribute to stakeholders as required"
    echo "4. Archive for compliance and audit requirements"
    echo ""
    
    if [[ "$DISTRIBUTE" == "true" ]]; then
        echo "=== DISTRIBUTION STATUS ==="
        echo "Bundle has been distributed to configured locations."
        echo "Check distribution directories for successful copies."
        echo ""
    fi
    
    echo "=== CONTACT INFORMATION ==="
    echo "For questions about this bundle:"
    echo "- Launch Team: launch-team@cortex.local"
    echo "- Technical Issues: engineering@cortex.local"
    echo "- Process Questions: governance@cortex.local"
    echo ""
}

# Main execution
main() {
    echo "Cortex Launch Evidence Bundle Packaging Script"
    echo "=============================================="
    echo ""
    
    validate_environment
    create_output_dir
    
    # Create temporary bundle directory
    local bundle_dir="$OUTPUT_DIR/$BUNDLE_NAME"
    mkdir -p "$bundle_dir"
    
    # Package the bundle
    copy_evidence_files "$bundle_dir" || exit 1
    generate_manifest "$bundle_dir"
    create_readme "$bundle_dir"
    generate_checksums "$bundle_dir"
    verify_bundle "$bundle_dir"
    create_packages "$bundle_dir" "$BUNDLE_NAME"
    distribute_bundle
    
    # Generate summary
    generate_summary "$bundle_dir"
}

# Execute main function
main "$@"