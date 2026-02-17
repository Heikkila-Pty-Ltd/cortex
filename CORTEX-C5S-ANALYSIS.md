# Epic Breakdown Analysis: cortex-a4s "Scrum master as project point-of-contact via Matrix"

## Current Issues

The epic cortex-a4s already has 11 child tasks, but they have several problems preventing execution by overnight automation:

### 1. **Epic Blocking Children**
All child tasks show "blocked by: cortex-a4s", preventing them from being worked on. The epic needs to be transitioned from blocking to guiding.

### 2. **Tasks Too Complex**
Example: cortex-a4s.9 "Implement scrum master sprint planning function" attempts to build:
- Configuration system for sprint planning
- SprintPlanner struct with multiple methods  
- Store integration for tracking planning
- Scheduler integration with triggers
- Prompt template system
- Matrix integration and reporting

This is more like an epic itself, not a focused task.

### 3. **Wrong Task Types**
Some tasks are typed as "feature" instead of "task" or "bug", which may confuse automated assignment.

### 4. **Complex Dependencies**
The dependency graph is complex with many interdependencies that may prevent parallel work.

## Solution: Task Refinement and Decomposition

### Phase 1: Foundation Tasks (No Dependencies)

**A. Configuration Infrastructure**
- Add Matrix room config to Project struct (cortex-a4s.1 - already well-scoped)
- Update scrum master ROLE.md (cortex-a4s.7 - already well-scoped)

### Phase 2: Core Infrastructure 

**B. Context and State Management**
- Build ProjectContext struct and queries (from cortex-a4s.2, simplified)
- Add project state formatting utilities

**C. Message Routing**  
- Refactor Reporter to route per-project (from cortex-a4s.3, simplified)
- Add Matrix inbound polling (from cortex-a4s.5, simplified)

### Phase 3: Communication Features (Depends on Phase 2)

**D. Outbound Notifications**
- Basic progress reports (simplified from cortex-a4s.4)
- Blocker alerts
- Decision requests

**E. Inbound Command Processing**
- Priority change commands (simplified from cortex-a4s.6)
- Status query commands
- Task creation commands

### Phase 4: Advanced Features (Optional)

**F. Sprint Management**
- Daily standup reports (simplified from cortex-a4s.8)
- Sprint planning workflow (break down cortex-a4s.9 into 3-4 tasks)
- Sprint retrospectives (break down cortex-a4s.11 into 2-3 tasks)

**G. Performance Optimization**
- Tier-based dispatch routing (cortex-a4s.10)

## Recommended Action

1. **Close the epic** as "decomposed into executable tasks"
2. **Create new focused tasks** replacing the overly complex ones
3. **Keep simple tasks** that are already well-scoped (a4s.1, a4s.7)
4. **Remove epic blocking** so tasks can be worked on independently

This approach transforms 11 complex, blocked tasks into ~15-20 focused, executable tasks that overnight automation can handle reliably.