package capture

// Source: apps/mekugi/internal/router/exec_inventory.go:1:59@1e6d58c6.

import "slices"

// Retained records from the former workspace observer remain decodable.
// No new workspace inventory is produced or compared.
const InventoryNote = "observed during command window"

type DirectoryStamp struct {
	Exists   bool
	Stamp    string
	Digest   string
	Complete bool
}

type Inventory struct {
	// Version 1 verifies blob bytes and uses change-clock stamps. Older inventories
	// remain readable, but their unverified blobs and weak stamps are not evidence.
	Version     int                       `json:",omitzero"`
	Directories map[string]DirectoryStamp `json:",omitempty"`
	Root        string
	// Entries maps each walked file's path relative to Root to its stamp.
	Entries map[string]string `json:",omitempty"`
	// Blobs maps tracked regular files whose content matched the git index
	// to their blob ids.
	Blobs map[string]string `json:",omitempty"`
	// Files holds bounded content not represented by a verified Git blob.
	Files []File `json:",omitempty"`
	// Repositories lists the git work trees nested below Root, relative to
	// it. Their files' blob ids name objects of the innermost one.
	Repositories []string `json:",omitempty"`
	// Pruned lists the relative paths the walk skipped: VCS metadata,
	// dependency trees. Ordinary ignored files remain eligible for capture.
	Pruned  []string   `json:",omitempty"`
	Omitted []Omission `json:",omitempty"`
}

// durable drops request-local preview stamps and spells empty collections as
// they read back.
func (i *Inventory) Durable() *Inventory {
	if i == nil {
		return nil
	}
	inventory := *i
	inventory.Files = slices.Clone(inventory.Files)
	for index := range inventory.Files {
		inventory.Files[index].WatchStamp = ""
	}
	if len(inventory.Files) == 0 {
		inventory.Files = nil
	}
	if len(inventory.Entries) == 0 {
		inventory.Entries = nil
	}
	if len(inventory.Blobs) == 0 {
		inventory.Blobs = nil
	}
	return &inventory
}
