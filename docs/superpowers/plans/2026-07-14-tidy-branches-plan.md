# Repository Branch Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tidy up the repository branches to match the separation of concerns for local runtime Phases P1-P7, removing redundant `-real` and `-finish` suffixes, and ensuring the clean branch names point to the correct implementations.

**Architecture:** Create/update local branches to point to the correct implementation commits for P2-P7. Delete the obsolete local branch `a-` and attempt to clean up and update the remote branches on `origin`.

**Tech Stack:** Git CLI

## Global Constraints
- Target commits must be exactly:
  * P2: `ff68e8125de6b4cdec1f872c5b632aa5ad04fb36`
  * P3: `6aafd2d1654f20e08d3d3198a76c7dfb894ff3b0`
  * P4: `d555919912705153f4d224899f35d1f203be08a7`
  * P5: `525ecda1db703ee50f9923db261835464b1555c9`
  * P6: `525ecda1db703ee50f9923db261835464b1555c9`
  * P7: `0955b74236cf64892c82c4b6cac1b2624e3e717c`
- Clean branch names (without suffix) must be used.
- Any remote changes should be pushed to `origin` if credentials/permissions allow.

---

### Task 1: Commit Spec and Align Local Branches

**Files:**
- Create: None
- Modify: None
- Test: Git branch status commands

**Interfaces:**
- Consumes: Commit SHAs resolved from `git log`
- Produces: Correctly aligned local branches: `p2-local-storage-replica`, `p3-local-runtime-event-bus-scheduler`, `p4-local-runtime-sync-engine`, `p5-local-runtime-org-bootstrap-multi-org`, `p6-local-runtime-coordination-server-hardening`, `p7-local-runtime-launch`.

- [ ] **Step 1: Commit the design specification file**
  
  Run: `git add docs/superpowers/specs/2026-07-14-tidy-branches-design.md`
  Run: `git commit -m "docs(specs): branch alignment design spec"`
  
- [ ] **Step 2: Create/Update local branches for P2 to P7**

  Run: `git branch -f p2-local-storage-replica ff68e8125de6b4cdec1f872c5b632aa5ad04fb36`
  Run: `git branch -f p3-local-runtime-event-bus-scheduler 6aafd2d1654f20e08d3d3198a76c7dfb894ff3b0`
  Run: `git branch -f p4-local-runtime-sync-engine d555919912705153f4d224899f35d1f203be08a7`
  Run: `git branch -f p5-local-runtime-org-bootstrap-multi-org 525ecda1db703ee50f9923db261835464b1555c9`
  Run: `git branch -f p6-local-runtime-coordination-server-hardening 525ecda1db703ee50f9923db261835464b1555c9`
  Run: `git branch -f p7-local-runtime-launch 0955b74236cf64892c82c4b6cac1b2624e3e717c`

- [ ] **Step 3: Delete local branch a-**

  Run: `git branch -D a-`

- [ ] **Step 4: Verify local branches**

  Run: `git branch`
  Expected: Branches `p2-...`, `p3-...`, `p4-...`, `p5-...`, `p6-...`, `p7-...` exist, and `a-` does not exist.

---

### Task 2: Remote Branch Alignment

**Files:**
- Create: None
- Modify: None
- Test: Git remote tracking status

**Interfaces:**
- Consumes: Local branches from Task 1
- Produces: Updated remote branches on `origin`

- [ ] **Step 1: Push new clean remote branches**

  Run: `git push origin p2-local-storage-replica p3-local-runtime-event-bus-scheduler p4-local-runtime-sync-engine p5-local-runtime-org-bootstrap-multi-org p6-local-runtime-coordination-server-hardening p7-local-runtime-launch --force`

- [ ] **Step 2: Delete redundant remote branches**

  Run: `git push origin --delete p3-local-runtime-event-bus-scheduler-finish p3-local-runtime-event-bus-scheduler-real p4-local-runtime-sync-engine-real p5-local-runtime-org-bootstrap-multi-org-real p6-local-runtime-coordination-server-hardening-real p7-local-runtime-launch-real`

- [ ] **Step 3: Verify remote branches match separation of concerns**
  
  Run: `git branch -r`
  Expected: Clean branches exist on remote, and suffix branches are gone.
