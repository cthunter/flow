// (c) Copyright 2015-2017 JONNALAGADDA Srinivas
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package flow

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

// WorkflowID is the type of unique workflow identifiers.
type WorkflowID int64

// Workflow represents the entire life cycle of a single document.
//
// A workflow begins with the creation of a document, and drives its
// life cycle through a sequence of responses to user actions or other
// system events.
//
// The engine in `flow` is visible primarily through workflows,
// documents and their behaviour.
//
// Currently, the topology of workflows is a graph, and is determined
// by the node definitions herein.
//
// N.B. It is highly recommended, but not necessary, that workflow
// names be defined in a system of hierarchical namespaces.
type Workflow struct {
	id     WorkflowID // Globally-unique identifier of this workflow
	name   string     // Globally-unique name of this workflow
	dtype  DocTypeID  // Document type of which this workflow defines the life cycle
	bstate DocStateID // Where this flow begins
}

// ID answers the unique identifier of this workflow.
func (w *Workflow) ID() WorkflowID {
	return w.id
}

// Name answers the globally-unique name of this workflow.
func (w *Workflow) Name() string {
	return w.name
}

// DocType answers the document type for which this defines the
// workflow.
func (w *Workflow) DocType() DocTypeID {
	return w.dtype
}

// BeginState answers the document state in which the execution of
// this workflow begins.
func (w *Workflow) BeginState() DocStateID {
	return w.bstate
}

// ApplyEvent takes an input user action or a system event, and
// applies its document action to the given document.  This results in
// a possibly new document state.  This method also prepares a message
// that is posted to applicable mailboxes.
func (w *Workflow) ApplyEvent(otx *sql.Tx, event *DocEvent, recipients []GroupID) (DocStateID, error) {
	if event == nil {
		return 0, errors.New("event should be non-nil")
	}
	if len(recipients) == 0 {
		return 0, errors.New("list of recipients should have length > 0")
	}
	if event.status == EventStatusApplied {
		return 0, errors.New("event already applied; nothing to do")
	}
	if w.dtype != event.dtype {
		return 0, fmt.Errorf("document type mismatch -- workflow's document type : %d, event's document type : %d", w.dtype, event.dtype)
	}

	n, err := _nodes.GetByState(w.dtype, event.state)
	if err != nil {
		return 0, err
	}

	var tx *sql.Tx
	if otx == nil {
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
	} else {
		tx = otx
	}

	nstate, err := n.applyEvent(tx, event, recipients)
	if err != nil {
		return 0, err
	}

	if otx == nil {
		err = tx.Commit()
		if err != nil {
			return 0, err
		}
	}

	return nstate, nil
}

// Unexported type, only for convenience methods.
type _Workflows struct{}

var _workflows *_Workflows

func init() {
	_workflows = &_Workflows{}
}

// Workflows provides a resource-like interface to the workflows
// defined in this system.
func Workflows() *_Workflows {
	return _workflows
}

// New creates and initialises a workflow definition using the given
// name, the document type whose life cycle this workflow should
// manage, and the initial document state in which this workflow
// begins.
//
// N.B.  Workflow names must be globally-unique.
func (ws *_Workflows) New(otx *sql.Tx, name string, dtype DocTypeID, state DocStateID) (WorkflowID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("name should not be empty")
	}

	var tx *sql.Tx
	if otx == nil {
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
	} else {
		tx = otx
	}

	q := `
	INSERT INTO wf_workflows(name, doctype_id, docstate_id)
	VALUES(?, ?, ?)
	`
	res, err := tx.Exec(q, name, dtype, state)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if otx == nil {
		err = tx.Commit()
		if err != nil {
			return 0, err
		}
	}

	return WorkflowID(id), nil
}

// List answers a subset of the workflows defined in the system,
// according to the given specification.
//
// Result set begins with ID >= `offset`, and has not more than
// `limit` elements.  A value of `0` for `offset` fetches from the
// beginning, while a value of `0` for `limit` fetches until the end.
func (ws *_Workflows) List(offset, limit int64) ([]*Workflow, error) {
	if offset < 0 || limit < 0 {
		return nil, errors.New("offset and limit must be non-negative integers")
	}
	if limit == 0 {
		limit = math.MaxInt64
	}

	q := `
	SELECT id, name, doctype_id, docstate_id
	FROM wf_workflows
	ORDER BY id
	LIMIT ? OFFSET ?
	`
	rows, err := db.Query(q, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ary := make([]*Workflow, 0, 10)
	for rows.Next() {
		var elem Workflow
		err = rows.Scan(&elem.id, &elem.name, &elem.dtype, &elem.bstate)
		if err != nil {
			return nil, err
		}
		ary = append(ary, &elem)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}

	return ary, nil
}

// Get retrieves the details of the requested workflow from the
// database.
//
// N.B.  This method retrieves the primary information of the
// workflow.  Information of the nodes comprising this workflow have
// to be fetched separately.
func (ws *_Workflows) Get(id WorkflowID) (*Workflow, error) {
	q := `
	SELECT name, doctype_id, docstate_id
	FROM wf_workflows
	WHERE id = ?
	`
	row := db.QueryRow(q, id)
	var elem Workflow
	err := row.Scan(&elem.name, &elem.dtype, &elem.bstate)
	if err != nil {
		return nil, err
	}

	elem.id = id
	return &elem, nil
}

// AddNode maps the given document state to the specified node.  This
// map is consulted by the workflow when performing a state transition
// of the system.
func (ws *_Workflows) AddNode(otx *sql.Tx, dtype DocTypeID, state DocStateID, wid WorkflowID,
	name string, ntype NodeType, hash map[DocActionID]DocStateID) (NodeID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, errors.New("name should not be empty")
	}
	if len(hash) == 0 {
		return 0, errors.New("transitions map should have length > 0")
	}

	var tx *sql.Tx
	if otx == nil {
		tx, err := db.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
	} else {
		tx = otx
	}

	q := `
	INSERT INTO wf_workflow_nodes(doctype_id, docstate_id, workflow_id, name, type)
	VALUES(?, ?, ?, ?, ?)
	`
	res, err := tx.Exec(q, dtype, state, wid, name, ntype)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	q = `
	INSERT INTO wf_docstate_transitions(doctype_id, from_state_id, docaction_id, to_state_id)
	VALUES(?, ?, ?, ?)
	`
	for da, ds := range hash {
		_, err := tx.Exec(q, dtype, state, da, ds)
		if err != nil {
			return 0, err
		}
	}

	if otx == nil {
		err = tx.Commit()
		if err != nil {
			return 0, err
		}
	}

	return NodeID(id), nil
}
