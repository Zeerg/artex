package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Zeerg/artex/db"
)

// Overview of the artificial CRUD interfaces for 'target management.' Write the same batch of goal nodes with agent-side set_goals tool.,
// But the entry is human-made through direct addition/deletion/modification in the UI.;Reuse 'resurrect task' logic after adding or modifying.(admitTask resume:
// Export→running, Resume paused tasks as needed.),Delete without resurrection(Make a decision based on the product). Each change handler goes through
// beginTaskOperation/decInflight,Avoid race condition with task deletion(Consistent with intent CRUD.).

// listGoals Return all targets of this task(text/vulnclass/state Set up),for rendering the target management card.
func (s *Server) listGoals(w http.ResponseWriter, r *http.Request) {
	t, ok:= s.m.Task(r.PathValue("id"))
	if!ok {
		writeErr(w, 404, "task not found")
		return
	}
	goals, err:= t.Store.ListByKind(db.KindGoal, 10000)
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"goals": goalDTOs(goals)})
}

// addGoal Manually add a new target:Data Exfiltration(Attached to the task root spawns under)→ Log a 'New target added' trigger to wake up
// planner → revive task,Let the planner re-evaluate whether it has been achieved based on the new target.
func (s *Server) addGoal(w http.ResponseWriter, r *http.Request) {
	t, ok:= s.m.Task(r.PathValue("id"))
	if!ok {
		writeErr(w, 404, "task not found")
		return
	}
	if!s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Task is being deleted,Unable to add new target")
		return
	}
	defer s.engine.decInflight(t.ID)

	var body struct {
		Text string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err:= json.NewDecoder(r.Body).Decode(&body); err!= nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text:= strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Target content cannot be empty")
		return
	}
	payload:= map[string]any{"text": text}
	if vc:= strings.TrimSpace(body.VulnClass); vc!= "" {
		payload["vulnclass"] = vc
	}
	id, err:= t.Store.AddGoal(payload, "human")
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	if of, _:= t.Store.OriginFactID(); of > 0 && id > 0 {
		_ = t.Store.Link(of, db.RelSpawns, id) // goal descends from the task root (origin fact)
	}
	t.NotifyGoal([]string{text}) // Remember 'a new target was added':...'Trigger and wake up planner.
	s.reviveTask(t) // Pull back completed/paused tasks to run state and continue running
	node, _:= t.Store.GetNode(id)
	if node == nil {
		writeErr(w, 500, "Target write failure after reading")
		return
	}
	writeJSON(w, 200, goalDTO(node))
}

// editGoal manually modify a target text(Including vulnclass):Change Database → Remember 'user changed target from old to new'
// Trigger planner wake-up → revive task,Let the planner adjust direction based on new targets.
func (s *Server) editGoal(w http.ResponseWriter, r *http.Request) {
	t, ok:= s.m.Task(r.PathValue("id"))
	if!ok {
		writeErr(w, 404, "task not found")
		return
	}
	if!s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Task is being deleted,Unable to modify target")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err:= strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err!= nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	var body struct {
		Text string `json:"text"`
		VulnClass string `json:"vulnclass"`
	}
	if err:= json.NewDecoder(r.Body).Decode(&body); err!= nil {
		writeErr(w, 400, "invalid JSON")
		return
	}
	text:= strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, 400, "Target content cannot be empty")
		return
	}
	node, err:= t.Store.GetNode(gid)
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind!= db.KindGoal {
		writeErr(w, 404, "Target does not exist")
		return
	}
	oldText:= goalDTO(node).Text
	if err:= t.Store.UpdateGoalPayload(gid, text, strings.TrimSpace(body.VulnClass)); err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalEdited(oldText, text) // Log 'a user changed the target from old to new' triggering and waking up the planner
	s.reviveTask(t) // Consistent with new addition:Rejudge the task based on the new target.
	updated, _:= t.Store.GetNode(gid)
	if updated == nil {
		writeErr(w, 500, "Target update failed to read")
		return
	}
	writeJSON(w, 200, goalDTO(updated))
}

// deleteGoal Manually delete a target(Hard delete,Cascade delete edges/anchors.):Data Exfiltration → Log 'User deleted target X'
// Trigger planner reevaluation based on this. Based on product decisions.,Delete the task without reviving it.
func (s *Server) deleteGoal(w http.ResponseWriter, r *http.Request) {
	t, ok:= s.m.Task(r.PathValue("id"))
	if!ok {
		writeErr(w, 404, "task not found")
		return
	}
	if!s.engine.beginTaskOperation(t.ID) {
		writeErr(w, 409, "Task is being deleted,Unable to delete target")
		return
	}
	defer s.engine.decInflight(t.ID)

	gid, err:= strconv.ParseInt(r.PathValue("gid"), 10, 64)
	if err!= nil || gid <= 0 {
		writeErr(w, 400, "bad goal id")
		return
	}
	node, err:= t.Store.GetNode(gid)
	if err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	if node == nil || node.Kind!= db.KindGoal {
		writeErr(w, 404, "Target does not exist")
		return
	}
	text:= goalDTO(node).Text
	if err:= t.Store.DeleteGoal(gid); err!= nil {
		writeErr(w, 500, err.Error())
		return
	}
	t.NotifyGoalDeleted(text) // Remember 'user deleted this target'.:...'Trigger and wake up planner.(Do not revive task)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
