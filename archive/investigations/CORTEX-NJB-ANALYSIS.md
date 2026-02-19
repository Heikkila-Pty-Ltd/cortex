# Epic Breakdown Analysis: cortex-xhk "LeSS coordination layer: cross-team orchestration"

## Current Issues

The epic cortex-xhk already has 11 child tasks, but they have several problems preventing execution by overnight automation:

### 1. **Epic Blocking Children**
All child tasks show "blocked by: cortex-xhk", preventing them from being worked on. The epic needs to be transitioned from blocking to guiding.

### 2. **Tasks Too Complex** 
Example: cortex-xhk.3 "Implement Chief Scrum Master agent" attempts to build:
- New agent role and ROLE.md configuration
- ChiefSM struct with multiple methods
- PortfolioContext data structures  
- Configuration parsing for chief scrum master
- Agent creation and startup integration
- Multiple supporting data types

This is more like an epic itself, not a focused task.

### 3. **Wrong Task Types**
Some tasks are typed as "feature" instead of "task", which may confuse automated assignment.

### 4. **Complex Multi-System Integration**
Example: cortex-xhk.6 "Multi-team sprint planning ceremony" requires:
- Portfolio context gathering
- Cross-project dependency analysis
- Capacity budget calculations
- Provider performance integration
- LLM dispatch coordination
- Matrix room integration
- Store recording of allocations

Each of these could be individual tasks.

### 5. **Cross-System Changes**
Many tasks require changes to multiple unrelated files:
- Rate limiting system
- Configuration parsing
- Agent management
- Scheduler integration
- API endpoints
- Store queries

## Solution: Task Decomposition by System

### Phase 1: Foundation Infrastructure

**A. Configuration Extensions**
- Add chief scrum master config schema
- Add rate limit budget configuration  
- Add sprint cadence configuration

**B. Data Models**
- Create basic cross-project data structures
- Add cross-project dependency tracking queries
- Add project summary statistics queries

### Phase 2: Core Services

**C. Rate Limit Budgeting**
- Add per-project rate limit tracking
- Implement budget enforcement
- Add budget rebalancing API

**D. Chief Scrum Master Agent**  
- Create chief scrum master role definition
- Add agent creation and configuration
- Add basic portfolio context gathering

### Phase 3: Coordination Features

**E. Sprint Cadence Alignment**
- Add sprint timing coordination
- Implement cross-project sprint scheduling
- Add cadence configuration enforcement

**F. Cross-Project Ceremonies**
- Basic multi-team sprint planning
- Cross-project retrospectives  
- Unified sprint reviews

### Phase 4: Advanced Features

**G. Performance Profiling**
- Cross-project provider analytics
- Performance trend analysis
- Provider recommendation system

**H. Predictive Planning**
- Capacity forecasting
- Workload prediction
- Resource optimization

## Root Cause: Architecture Complexity

The LeSS coordination layer is inherently complex because it requires:
1. **Multi-project visibility** across previously isolated systems
2. **Cross-cutting concerns** affecting scheduler, config, store, agents
3. **Ceremony orchestration** involving multiple LLM dispatches
4. **Resource coordination** with budget allocation and rebalancing

## Recommended Decomposition Strategy

Break each existing complex task into 3-4 focused tasks:

### Instead of "Implement Chief Scrum Master agent" (cortex-xhk.3):
1. **Add chief scrum master role configuration**
2. **Create basic portfolio context queries** 
3. **Add chief agent creation and startup**
4. **Integrate chief agent with Matrix coordination**

### Instead of "Multi-team sprint planning ceremony" (cortex-xhk.6):
1. **Add portfolio backlog gathering**
2. **Create sprint planning prompt template**
3. **Add capacity allocation logic**
4. **Integrate multi-team planning with scheduler**

### Instead of "Rate limit capacity budgeting" (cortex-xhk.4):
1. **Add budget configuration parsing**
2. **Implement per-project usage tracking**
3. **Add budget enforcement to scheduler**  
4. **Create budget rebalancing API**

This transforms 11 complex, blocked tasks into ~35-40 focused, executable tasks.

## Benefits of Decomposition

1. **Reduced Risk**: Each task has single, clear responsibility
2. **Testable Components**: Focused scope enables thorough testing
3. **Parallel Development**: Independent tasks can be worked simultaneously
4. **Clear Progress**: Each completed task provides measurable advancement
5. **Manageable Size**: Each task completable in <2 hours focused work

## Recommendation

1. **Close the epic** as "decomposed into executable tasks"
2. **Create new focused tasks** replacing the complex ones
3. **Keep well-scoped tasks** (if any exist)
4. **Remove epic blocking** so tasks can be worked on independently
5. **Set logical dependencies** to enable proper sequencing

This approach transforms an unmanageable, blocked epic into a series of focused, executable work items that overnight automation can handle reliably.