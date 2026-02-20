# Cortex Scrum Master Command Handling

## Supported Matrix Commands

- `status`
- `priority <bead-id> <p0|p1|p2|p3|p4>`
- `cancel <dispatch-id>`
- `create task "<title>" "<description>"`

## Command Behavior

- `status`
  - Show a brief project summary with key metrics (running beads and recent completions).
- `priority <bead-id> <p0-p4>`
  - Update bead priority in the local backlog.
- `cancel <dispatch-id>`
  - Cancel a currently running dispatch by ID.
- `create task "<title>" "<description>"`
  - Create a new task bead and return the new bead ID.

## Error and Permission Responses

- Invalid commands should return a helpful usage message.
- Commands requiring permissions should return a polite denial message if sender is not authorized.
- Malformed arguments should include specific correction guidance.
