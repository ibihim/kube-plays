#!/bin/bash

# Check if a search term was provided
if [ $# -eq 0 ]; then
    echo "Please provide a search term"
    echo "Usage: $0 <search_term>"
    exit 1
fi

SEARCH_TERM="$1"

# Get the list of all ClusterOperators
OPERATORS=$(oc get clusteroperator -o name)

# Loop through each operator
for OPERATOR in $OPERATORS; do
    echo "Searching $OPERATOR for '$SEARCH_TERM'..."
    
    # Get the operator's YAML and search for the term in the status section
    RESULT=$(oc get $OPERATOR -o yaml | grep -i "$SEARCH_TERM")
    
    if [ -n "$RESULT" ]; then
        echo "Found in $OPERATOR:"
        echo "$RESULT"
        echo "---"
    fi
done

echo "Search complete."
