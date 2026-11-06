package handlers

import (
	"context"
	"queuebot/access"
)

type testAccessControl struct {
	AccessControl
	admins map[int64]bool
}

var _ AccessControl = (*testAccessControl)(nil)

func (control *testAccessControl) Allowed(_ context.Context, id int64, _ access.Permission) (bool, error) {
	return id == 789915524 || control.admins[id], nil
}
