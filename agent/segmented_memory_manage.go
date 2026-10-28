package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func validSegmentedMemoryID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func findSegmentedMemoryInteractionDir(root, interactionID string) (string, error) {
	if !validSegmentedMemoryID(interactionID) {
		return "", fmt.Errorf("invalid interaction_id")
	}
	// New hierarchy first. The directory name is unique within a day; scan
	// daily buckets so callers only need the interaction id from the UI.
	sessionsRoot := filepath.Join(root, segmentedMemorySessionsDir)
	var found string
	_ = filepath.WalkDir(sessionsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.IsDir() || found != "" {
			return nil
		}
		if entry.Name() != interactionID {
			return nil
		}
		parent := filepath.Base(filepath.Dir(path))
		if parent != segmentedMemoryInteractionsDir {
			return nil
		}
		found = path
		return fs.SkipAll
	})
	if found != "" {
		return found, nil
	}
	// Legacy layout.
	legacy := filepath.Join(root, segmentedMemorySegmentDir, interactionID)
	if info, err := os.Stat(legacy); err == nil && info.IsDir() {
		return legacy, nil
	}
	return "", os.ErrNotExist
}

func readSegmentedMemorySegmentAt(path string) (*segmentedMemorySegment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var segment segmentedMemorySegment
	if err := json.Unmarshal(data, &segment); err != nil {
		return nil, err
	}
	if strings.TrimSpace(segment.Status) == "" {
		segment.Status = segmentStatusActive
	}
	return &segment, nil
}

func writeSegmentedMemorySegmentAt(path string, segment *segmentedMemorySegment) error {
	if segment == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	data, err := json.MarshalIndent(segment, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func findSegmentedMemorySegment(root, interactionID, segmentID string) (*segmentedMemorySegment, string, error) {
	if !validSegmentedMemoryID(segmentID) {
		return nil, "", fmt.Errorf("invalid segment_id")
	}
	var searchRoot string
	if interactionID != "" {
		dir, err := findSegmentedMemoryInteractionDir(root, interactionID)
		if err != nil {
			return nil, "", err
		}
		searchRoot = dir
	} else {
		// Segment ids are local to an interaction. Scan daily buckets in
		// newest-first order so the common case (recent memory) is fast.
		searchRoot = filepath.Join(root, segmentedMemorySessionsDir)
	}
	var foundPath string
	_ = filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".json" || filepath.Base(path) == segmentedMemoryManifestFile {
			return nil
		}
		segment, err := readSegmentedMemorySegmentAt(path)
		if err != nil || segment.ID != segmentID {
			return nil
		}
		if interactionID != "" && segmentedMemorySegmentInteractionID(segment) != interactionID {
			return nil
		}
		foundPath = path
		return fs.SkipAll
	})
	if foundPath == "" {
		// Legacy layout.
		_ = filepath.WalkDir(filepath.Join(root, segmentedMemorySegmentDir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".json" {
				return nil
			}
			segment, err := readSegmentedMemorySegmentAt(path)
			if err != nil || segment.ID != segmentID {
				return nil
			}
			if interactionID != "" && segmentedMemorySegmentInteractionID(segment) != interactionID {
				return nil
			}
			foundPath = path
			return fs.SkipAll
		})
	}
	if foundPath == "" {
		return nil, "", os.ErrNotExist
	}
	segment, err := readSegmentedMemorySegmentAt(foundPath)
	return segment, foundPath, err
}

func updateSegmentedIndexEntries(root string, mutate func(*segmentedMemoryIndexEntry) bool) (int, error) {
	segmentedIndexMu.Lock()
	defer segmentedIndexMu.Unlock()

	paths := []string{filepath.Join(root, segmentedMemoryIndexDir, "root.jsonl")}
	volumeDir := filepath.Join(root, segmentedMemoryIndexDir, segmentedMemoryVolumeDir)
	_ = filepath.WalkDir(volumeDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	changed := 0
	for _, path := range paths {
		lines, err := readSegmentedIndexLines(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return changed, err
		}
		out := make([]string, 0, len(lines))
		fileChanged := false
		for _, line := range lines {
			var entry segmentedMemoryIndexEntry
			if json.Unmarshal([]byte(line), &entry) != nil {
				out = append(out, line)
				continue
			}
			if mutate(&entry) {
				data, marshalErr := json.Marshal(entry)
				if marshalErr != nil {
					return changed, marshalErr
				}
				out = append(out, string(data))
				fileChanged = true
				changed++
				continue
			}
			out = append(out, line)
		}
		if !fileChanged {
			continue
		}
		content := strings.Join(out, "\n")
		if content != "" {
			content += "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

func removeSegmentedIndexEntries(root string, match func(*segmentedMemoryIndexEntry) bool) (int, error) {
	segmentedIndexMu.Lock()
	defer segmentedIndexMu.Unlock()

	paths := []string{filepath.Join(root, segmentedMemoryIndexDir, "root.jsonl")}
	volumeDir := filepath.Join(root, segmentedMemoryIndexDir, segmentedMemoryVolumeDir)
	_ = filepath.WalkDir(volumeDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && strings.EqualFold(filepath.Ext(path), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	removed := 0
	for _, path := range paths {
		lines, err := readSegmentedIndexLines(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, err
		}
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			var entry segmentedMemoryIndexEntry
			if json.Unmarshal([]byte(line), &entry) == nil && match(&entry) {
				removed++
				continue
			}
			out = append(out, line)
		}
		if len(out) == len(lines) {
			continue
		}
		content := strings.Join(out, "\n")
		if content != "" {
			content += "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func segmentedIndexEntryTargets(index *segmentedMemoryIndexEntry, interactionID, segmentID string) bool {
	if index == nil {
		return false
	}
	entryInteraction := strings.TrimSpace(index.InteractionID)
	if entryInteraction == "" {
		entryInteraction = strings.TrimSpace(index.TurnID)
	}
	if interactionID != "" && entryInteraction != "" && entryInteraction != interactionID {
		return false
	}
	if segmentID != "" && strings.TrimSpace(index.SegmentID) != segmentID {
		return false
	}
	if interactionID != "" && entryInteraction == "" {
		target := filepath.ToSlash(strings.TrimSpace(index.Target))
		if !strings.Contains(target, "/interactions/"+interactionID+"/") && !strings.Contains(target, "/segments/"+interactionID+"/") {
			return false
		}
	}
	return true
}

func invalidateSegmentedMemorySegment(root, interactionID, segmentID, reason, replacementID, deletedBy string) (map[string]any, error) {
	if !validSegmentedMemoryID(segmentID) {
		return nil, fmt.Errorf("invalid segment_id")
	}
	segment, path, err := findSegmentedMemorySegment(root, interactionID, segmentID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if strings.TrimSpace(deletedBy) == "" {
		deletedBy = "model"
	}
	segment.Status = segmentStatusInvalidated
	segment.DeletedBy = deletedBy
	segment.DeletedAt = now
	segment.Reason = strings.TrimSpace(reason)
	segment.ReplacementID = strings.TrimSpace(replacementID)
	if err := writeSegmentedMemorySegmentAt(path, segment); err != nil {
		return nil, err
	}
	changed, err := updateSegmentedIndexEntries(root, func(entry *segmentedMemoryIndexEntry) bool {
		if !segmentedIndexEntryTargets(entry, segmentedMemorySegmentInteractionID(segment), segmentID) {
			return false
		}
		entry.Status = segmentStatusInvalidated
		entry.DeletedBy = deletedBy
		entry.DeletedAt = now
		entry.Reason = segment.Reason
		entry.ReplacementID = segment.ReplacementID
		return true
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":             true,
		"action":         "invalidated",
		"session_id":     segment.SessionID,
		"interaction_id": segmentedMemorySegmentInteractionID(segment),
		"segment_id":     segment.ID,
		"index_entries":  changed,
		"replacement_id": segment.ReplacementID,
	}, nil
}

func invalidateSegmentedMemoryInteraction(root, interactionID, reason, replacementID, deletedBy string) (map[string]any, error) {
	dir, err := findSegmentedMemoryInteractionDir(root, interactionID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	if strings.TrimSpace(deletedBy) == "" {
		deletedBy = "model"
	}
	segmentIDs := []string{}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".json" || filepath.Base(path) == segmentedMemoryManifestFile {
			return nil
		}
		segment, readErr := readSegmentedMemorySegmentAt(path)
		if readErr != nil {
			return nil
		}
		segment.Status = segmentStatusInvalidated
		segment.DeletedBy = deletedBy
		segment.DeletedAt = now
		segment.Reason = strings.TrimSpace(reason)
		segment.ReplacementID = strings.TrimSpace(replacementID)
		if writeErr := writeSegmentedMemorySegmentAt(path, segment); writeErr != nil {
			return writeErr
		}
		segmentIDs = append(segmentIDs, segment.ID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(segmentIDs)
	changed := 0
	for _, segmentID := range segmentIDs {
		count, updateErr := updateSegmentedIndexEntries(root, func(entry *segmentedMemoryIndexEntry) bool {
			if !segmentedIndexEntryTargets(entry, interactionID, segmentID) {
				return false
			}
			entry.Status = segmentStatusInvalidated
			entry.DeletedBy = deletedBy
			entry.DeletedAt = now
			entry.Reason = strings.TrimSpace(reason)
			entry.ReplacementID = strings.TrimSpace(replacementID)
			return true
		})
		if updateErr != nil {
			return nil, updateErr
		}
		changed += count
	}
	_ = updateSegmentedMemoryManifest(root, interactionID, segmentStatusInvalidated, deletedBy, reason)
	return map[string]any{
		"ok":             true,
		"action":         "invalidated",
		"interaction_id": interactionID,
		"segment_ids":    segmentIDs,
		"index_entries":  changed,
	}, nil
}

func deleteSegmentedMemorySegment(root, interactionID, segmentID, reason string) (map[string]any, error) {
	segment, path, err := findSegmentedMemorySegment(root, interactionID, segmentID)
	if err != nil {
		return nil, err
	}
	resolvedInteraction := segmentedMemorySegmentInteractionID(segment)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	changed, err := removeSegmentedIndexEntries(root, func(entry *segmentedMemoryIndexEntry) bool {
		return segmentedIndexEntryTargets(entry, resolvedInteraction, segmentID)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":             true,
		"action":         "deleted",
		"session_id":     segment.SessionID,
		"interaction_id": resolvedInteraction,
		"segment_id":     segment.ID,
		"index_entries":  changed,
		"reason":         strings.TrimSpace(reason),
	}, nil
}

func deleteSegmentedMemoryInteraction(root, interactionID, reason string) (map[string]any, error) {
	dir, err := findSegmentedMemoryInteractionDir(root, interactionID)
	if err != nil {
		return nil, err
	}
	segmentIDs := []string{}
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || strings.ToLower(filepath.Ext(path)) != ".json" || filepath.Base(path) == segmentedMemoryManifestFile {
			return nil
		}
		if segment, readErr := readSegmentedMemorySegmentAt(path); readErr == nil {
			segmentIDs = append(segmentIDs, segment.ID)
		}
		return nil
	})
	sort.Strings(segmentIDs)
	changed := 0
	for _, segmentID := range segmentIDs {
		count, updateErr := removeSegmentedIndexEntries(root, func(entry *segmentedMemoryIndexEntry) bool {
			return segmentedIndexEntryTargets(entry, interactionID, segmentID)
		})
		if updateErr != nil {
			return nil, updateErr
		}
		changed += count
	}
	// Also remove entries that point at the interaction but somehow lack a
	// segment id (for example manually edited legacy indexes).
	count, updateErr := removeSegmentedIndexEntries(root, func(entry *segmentedMemoryIndexEntry) bool {
		return segmentedIndexEntryTargets(entry, interactionID, "")
	})
	if updateErr != nil {
		return nil, updateErr
	}
	changed += count
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":             true,
		"action":         "deleted",
		"interaction_id": interactionID,
		"segment_ids":    segmentIDs,
		"index_entries":  changed,
		"reason":         strings.TrimSpace(reason),
	}, nil
}

func updateSegmentedMemoryManifest(root, interactionID, status, deletedBy, reason string) error {
	dir, err := findSegmentedMemoryInteractionDir(root, interactionID)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, segmentedMemoryManifestFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var manifest segmentedMemoryManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	manifest.Status = status
	manifest.DeletedBy = deletedBy
	manifest.DeletedAt = time.Now().UnixMilli()
	manifest.Reason = strings.TrimSpace(reason)
	manifest.UpdatedAt = time.Now().UnixMilli()
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(updated, '\n'), 0o644)
}
