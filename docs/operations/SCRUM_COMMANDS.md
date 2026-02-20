# Cortex Scrum Master Command Handling

The scrum master handles direct Matrix commands for project management from polling input.

## Supported Commands

- `status`
- `priority <bead-id> <p0|p1|p2|p3|p4>`
- `cancel <dispatch-id>`
- `create task "<title>" "<description>"`

## Command Templates

- `status`
  - Returns a brief project summary with running bead count and recent completions.
- `priority <bead-id> <p0|p1|p2|p3|p4>`
  - Updates the bead priority and returns `Updated <bead-id> priority to <pX>`.
- `cancel <dispatch-id>`
  - Cancels the running dispatch and returns `Cancelled dispatch <dispatch-id>` or failure.
- `create task "<title>" "<description>"`
  - Creates a new task bead and returns `Created new task <bead-id>`.

## Error Handling

- Unknown or malformed commands should return usage guidance with supported commands.
- Missing permissions should return a polite denial plus usage guidance.
- Invalid argument shape should explain the expected form:
  - priority requires `p0` through `p4`
  - cancel requires a positive dispatch id
  - create task requires quoted title and description
