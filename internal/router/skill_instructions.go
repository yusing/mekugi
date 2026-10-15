package router

import (
	"errors"
	"os/exec"
	"path/filepath"
)

func skillsManagerInPath(extraDirectory string) bool {
	if path, err := exec.LookPath("skills-mgr"); err == nil || path != "" && errors.Is(err, exec.ErrDot) {
		return true
	}
	if extraDirectory == "" {
		return false
	}
	_, err := exec.LookPath(filepath.Join(extraDirectory, "skills-mgr"))
	return err == nil
}
