#!/bin/bash

# Launch Readiness Gate Evidence Collection Script
# Systematically collects evidence from all P0/P1 launch gates

set -euo pipefail

EVIDENCE_DIR="$(dirname "$0")"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
COLLECTION_LOG="$EVIDENCE_DIR/collection-log-$TIMESTAMP.json"
WORKSPACE_ROOT="$(cd "$EVIDENCE_DIR/../" && pwd)"

echo "🔍 Starting launch readiness gate evidence collection at $TIMESTAMP"
echo "📁 Evidence directory: $EVIDENCE_DIR"
echo "🏠 Workspace root: $WORKSPACE_ROOT"

# Initialize collection log
cat > "$COLLECTION_LOG" << EOF
{
  "collection_timestamp": "$TIMESTAMP",
  "workspace_root": "$WORKSPACE_ROOT",
  "gates": {},
  "validation_errors": [],
  "collection_summary": {}
}
EOF

# Evidence collection functions
collect_security_evidence() {
    echo "🔒 Collecting Security gate evidence..."
    local evidence=()
    
    # Authentication/Authorization implementation
    if [[ -f "$WORKSPACE_ROOT/docs/api-security.md" ]]; then
        evidence+=("docs/api-security.md")
        echo "  ✓ Found API security documentation"
    else
        echo "  ⚠️  Missing API security documentation"
        jq '.validation_errors += ["Missing docs/api-security.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Look for auth implementation files
    while IFS= read -r -d '' file; do
        # Convert to relative path
        rel_file="${file#$WORKSPACE_ROOT/}"
        evidence+=("$rel_file")
        echo "  ✓ Found auth implementation: $(basename "$file")"
    done < <(find "$WORKSPACE_ROOT" -name "*.go" -o -name "*.js" -o -name "*.py" | xargs grep -l "auth\|Auth" | head -5 | tr '\n' '\0')
    
    # Audit logging implementation
    while IFS= read -r -d '' file; do
        # Convert to relative path
        rel_file="${file#$WORKSPACE_ROOT/}"
        evidence+=("$rel_file")
        echo "  ✓ Found audit logging: $(basename "$file")"
    done < <(find "$WORKSPACE_ROOT" -name "*.go" -o -name "*.js" -o -name "*.py" | xargs grep -l "audit\|log" | head -5 | tr '\n' '\0')
    
    # Security scan results
    if [[ -f "security/scan-results.json" ]]; then
        evidence+=("security/scan-results.json")
        echo "  ✓ Found security scan results"
    else
        echo "  ⚠️  Missing security scan results"
        jq '.validation_errors += ["Missing security/scan-results.json"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Update collection log
    local evidence_json=$(printf '%s\n' "${evidence[@]}" | jq -R . | jq -s .)
    jq --argjson evidence "$evidence_json" '.gates.security = {
        "status": "collected",
        "evidence_count": ($evidence | length),
        "evidence_files": $evidence,
        "collection_timestamp": "'$TIMESTAMP'"
    }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

collect_reliability_evidence() {
    echo "⚡ Collecting Reliability gate evidence..."
    local evidence=()
    
    # Burn-in results
    local burnin_files=($(find "$WORKSPACE_ROOT/artifacts/launch/burnin" -name "*.json" -o -name "*.md" 2>/dev/null || true))
    if [[ ${#burnin_files[@]} -gt 0 ]]; then
        # Convert to relative paths
        for i in "${!burnin_files[@]}"; do
            burnin_files[$i]="${burnin_files[$i]#$WORKSPACE_ROOT/}"
        done
        evidence+=("${burnin_files[@]}")
        echo "  ✓ Found ${#burnin_files[@]} burn-in result files"
    else
        echo "  ⚠️  Missing burn-in results"
        jq '.validation_errors += ["Missing burn-in results in artifacts/launch/burnin"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # SLO scoring
    if [[ -f "slo/scoring-results.json" ]]; then
        evidence+=("slo/scoring-results.json")
        echo "  ✓ Found SLO scoring results"
    else
        echo "  ⚠️  Missing SLO scoring results"
        jq '.validation_errors += ["Missing slo/scoring-results.json"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Performance benchmarks
    while IFS= read -r -d '' file; do
        evidence+=("$file")
        echo "  ✓ Found benchmark: $(basename "$file")"
    done < <(find . -name "*bench*" -name "*.json" -o -name "*perf*" -name "*.json" | head -3 | tr '\n' '\0')
    
    # Update collection log
    local evidence_json=$(printf '%s\n' "${evidence[@]}" | jq -R . | jq -s .)
    jq --argjson evidence "$evidence_json" '.gates.reliability = {
        "status": "collected",
        "evidence_count": ($evidence | length),
        "evidence_files": $evidence,
        "collection_timestamp": "'$TIMESTAMP'"
    }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

collect_operations_evidence() {
    echo "⚙️  Collecting Operations gate evidence..."
    local evidence=()
    
    # Runbooks
    local runbook_files=($(find artifacts/launch/runbooks -name "*.md" -o -name "*.txt" 2>/dev/null || true))
    if [[ ${#runbook_files[@]} -gt 0 ]]; then
        evidence+=("${runbook_files[@]}")
        echo "  ✓ Found ${#runbook_files[@]} runbook files"
    else
        echo "  ⚠️  Missing operational runbooks"
        jq '.validation_errors += ["Missing runbooks in artifacts/launch/runbooks"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Operational readiness checklist
    if [[ -f "ops/readiness-checklist.md" ]]; then
        evidence+=("ops/readiness-checklist.md")
        echo "  ✓ Found operational readiness checklist"
    else
        echo "  ⚠️  Missing operational readiness checklist"
        jq '.validation_errors += ["Missing ops/readiness-checklist.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Monitoring setup
    if [[ -f "monitoring/setup.md" ]]; then
        evidence+=("monitoring/setup.md")
        echo "  ✓ Found monitoring setup documentation"
    else
        echo "  ⚠️  Missing monitoring setup documentation"
        jq '.validation_errors += ["Missing monitoring/setup.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Update collection log
    local evidence_json=$(printf '%s\n' "${evidence[@]}" | jq -R . | jq -s .)
    jq --argjson evidence "$evidence_json" '.gates.operations = {
        "status": "collected",
        "evidence_count": ($evidence | length),
        "evidence_files": $evidence,
        "collection_timestamp": "'$TIMESTAMP'"
    }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

collect_data_evidence() {
    echo "💾 Collecting Data gate evidence..."
    local evidence=()
    
    # Backup/restore validation
    if [[ -f "data/backup-restore-validation.md" ]]; then
        evidence+=("data/backup-restore-validation.md")
        echo "  ✓ Found backup/restore validation results"
    else
        echo "  ⚠️  Missing backup/restore validation"
        jq '.validation_errors += ["Missing data/backup-restore-validation.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Data protection measures
    if [[ -f "data/protection-measures.md" ]]; then
        evidence+=("data/protection-measures.md")
        echo "  ✓ Found data protection measures documentation"
    else
        echo "  ⚠️  Missing data protection measures"
        jq '.validation_errors += ["Missing data/protection-measures.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Data schema validation
    while IFS= read -r -d '' file; do
        evidence+=("$file")
        echo "  ✓ Found schema file: $(basename "$file")"
    done < <(find . -name "schema.json" -o -name "*schema*.sql" | head -3 | tr '\n' '\0')
    
    # Update collection log
    local evidence_json=$(printf '%s\n' "${evidence[@]}" | jq -R . | jq -s .)
    jq --argjson evidence "$evidence_json" '.gates.data = {
        "status": "collected",
        "evidence_count": ($evidence | length),
        "evidence_files": $evidence,
        "collection_timestamp": "'$TIMESTAMP'"
    }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

collect_release_evidence() {
    echo "🚀 Collecting Release gate evidence..."
    local evidence=()
    
    # Release process definition
    if [[ -f "release/process-definition.md" ]]; then
        evidence+=("release/process-definition.md")
        echo "  ✓ Found release process definition"
    else
        echo "  ⚠️  Missing release process definition"
        jq '.validation_errors += ["Missing release/process-definition.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Dry run results
    if [[ -f "release/dry-run-results.json" ]]; then
        evidence+=("release/dry-run-results.json")
        echo "  ✓ Found release dry run results"
    else
        echo "  ⚠️  Missing release dry run results"
        jq '.validation_errors += ["Missing release/dry-run-results.json"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Rollback procedures
    if [[ -f "release/rollback-procedures.md" ]]; then
        evidence+=("release/rollback-procedures.md")
        echo "  ✓ Found rollback procedures"
    else
        echo "  ⚠️  Missing rollback procedures"
        jq '.validation_errors += ["Missing release/rollback-procedures.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Update collection log
    local evidence_json=$(printf '%s\n' "${evidence[@]}" | jq -R . | jq -s .)
    jq --argjson evidence "$evidence_json" '.gates.release = {
        "status": "collected",
        "evidence_count": ($evidence | length),
        "evidence_files": $evidence,
        "collection_timestamp": "'$TIMESTAMP'"
    }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

collect_safety_evidence() {
    echo "🛡️  Collecting Safety gate evidence..."
    local evidence=()
    
    # LLM operator trial results
    if [[ -f "safety/llm-operator-trial-results.json" ]]; then
        evidence+=("safety/llm-operator-trial-results.json")
        echo "  ✓ Found LLM operator trial results"
    else
        echo "  ⚠️  Missing LLM operator trial results"
        jq '.validation_errors += ["Missing safety/llm-operator-trial-results.json"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Compliance documentation
    if [[ -f "safety/compliance-documentation.md" ]]; then
        evidence+=("safety/compliance-documentation.md")
        echo "  ✓ Found compliance documentation"
    else
        echo "  ⚠️  Missing compliance documentation"
        jq '.validation_errors += ["Missing safety/compliance-documentation.md"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Safety review results
    if [[ -f "safety/safety-review-results.json" ]]; then
        evidence+=("safety/safety-review-results.json")
        echo "  ✓ Found safety review results"
    else
        echo "  ⚠️  Missing safety review results"
        jq '.validation_errors += ["Missing safety/safety-review-results.json"]' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    fi
    
    # Update collection log
    local evidence_json=$(printf '%s\n' "${evidence[@]}" | jq -R . | jq -s .)
    jq --argjson evidence "$evidence_json" '.gates.safety = {
        "status": "collected",
        "evidence_count": ($evidence | length),
        "evidence_files": $evidence,
        "collection_timestamp": "'$TIMESTAMP'"
    }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

generate_summary() {
    echo "📊 Generating collection summary..."
    
    # Count total evidence files and validation errors
    local total_evidence=$(jq '[.gates[] | .evidence_count] | add' "$COLLECTION_LOG")
    local total_errors=$(jq '.validation_errors | length' "$COLLECTION_LOG")
    local total_gates=$(jq '.gates | length' "$COLLECTION_LOG")
    
    # Update summary in collection log
    jq --argjson total_evidence "$total_evidence" \
       --argjson total_errors "$total_errors" \
       --argjson total_gates "$total_gates" \
       '.collection_summary = {
           "total_gates": $total_gates,
           "total_evidence_files": $total_evidence,
           "total_validation_errors": $total_errors,
           "collection_complete": true
       }' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
    
    echo "✅ Collection complete:"
    echo "   📋 Gates processed: $total_gates"
    echo "   📄 Evidence files: $total_evidence"
    echo "   ⚠️  Validation errors: $total_errors"
    echo "   📝 Log saved to: $COLLECTION_LOG"
}

validate_evidence_accessibility() {
    echo "🔍 Validating evidence file accessibility..."
    
    local validation_results=()
    while read -r gate; do
        echo "  Validating $gate gate evidence..."
        while read -r file; do
            local full_path="$WORKSPACE_ROOT/$file"
            if [[ -f "$full_path" ]]; then
                local size=$(stat -c%s "$full_path" 2>/dev/null || echo "0")
                local mtime=$(stat -c%Y "$full_path" 2>/dev/null || echo "0")
                validation_results+=("\"$file\": {\"accessible\": true, \"size\": $size, \"mtime\": $mtime}")
                echo "    ✓ $file ($size bytes)"
            else
                validation_results+=("\"$file\": {\"accessible\": false, \"size\": 0, \"mtime\": 0}")
                echo "    ✗ $file (not found)"
            fi
        done < <(jq -r ".gates.$gate.evidence_files[]" "$COLLECTION_LOG")
    done < <(jq -r '.gates | keys[]' "$COLLECTION_LOG")
    
    # Add validation results to log
    local validation_json="{$(IFS=,; echo "${validation_results[*]}")}"
    jq --argjson validation "$validation_json" '.evidence_validation = $validation' "$COLLECTION_LOG" > tmp.json && mv tmp.json "$COLLECTION_LOG"
}

# Main execution
main() {
    collect_security_evidence
    collect_reliability_evidence
    collect_operations_evidence
    collect_data_evidence
    collect_release_evidence
    collect_safety_evidence
    
    validate_evidence_accessibility
    generate_summary
    
    echo ""
    echo "🎯 Next steps:"
    echo "1. Review validation errors in $COLLECTION_LOG"
    echo "2. Update launch-readiness-matrix.md with current gate status"
    echo "3. Generate validation-report.md with detailed findings"
    echo "4. Address any missing evidence before launch decision"
    
    return 0
}

# Run main function
main "$@"