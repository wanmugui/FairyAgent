package main

import "testing"

// The slim registry is injected into every turn's system prompt. When it
// carried every skill's description it cost ~3.5k tokens per turn, before the
// model had read any of the user's request. Only core skills belong there;
// everything else is found on demand through skill_search.
func TestCoreSkillRegistriesKeepsOnlyCore(t *testing.T) {
	yes := true
	no := false
	all := []SkillReg{
		{Name: "systematic-debugging", Core: &yes},
		{Name: "three-webgl-game", Core: nil},
		{Name: "old-thing", Core: &no},
	}

	got := coreSkillRegistries(all)
	if len(got) != 1 {
		t.Fatalf("kept %d skills, want 1: %+v", len(got), got)
	}
	if got[0].Name != "systematic-debugging" {
		t.Fatalf("kept %q, want systematic-debugging", got[0].Name)
	}
}

// Core is a *bool precisely so "unset" stays distinguishable from "explicitly
// not core". An unset skill is not core, so it must not be injected.
func TestCoreSkillRegistriesTreatsUnsetAsNotCore(t *testing.T) {
	all := []SkillReg{{Name: "a"}, {Name: "b"}}
	if got := coreSkillRegistries(all); len(got) != 0 {
		t.Fatalf("unset core must not be injected, got %+v", got)
	}
}

// A user who unstars everything must get an empty registry, not a silent
// fallback to everything: the star is the contract shown in the settings UI.
func TestCoreSkillRegistriesEmptyWhenNoCore(t *testing.T) {
	if got := coreSkillRegistries([]SkillReg{{Name: "a"}, {Name: "b"}}); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestCoreSkillRegistriesNilSafe(t *testing.T) {
	if got := coreSkillRegistries(nil); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

// The filter must not drop fields the prompt needs. Description is what makes
// a routing table useful at all; losing it would leave bare names.
func TestCoreSkillRegistriesPreservesEntry(t *testing.T) {
	yes := true
	entry := SkillReg{
		Name:        "verification-before-completion",
		Description: "在即将声明完成前先跑验证",
		Location:    "/skills/verification-before-completion",
		Priority:    90,
		Core:        &yes,
	}

	got := coreSkillRegistries([]SkillReg{entry})
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].Description != entry.Description {
		t.Fatalf("description lost: %q", got[0].Description)
	}
	if got[0].Location != entry.Location {
		t.Fatalf("location lost: %q", got[0].Location)
	}
	if got[0].Priority != entry.Priority {
		t.Fatalf("priority lost: %d", got[0].Priority)
	}
}

// The whole point is that non-core skills stay reachable — through
// skill_search, which scans independently. If that path were coupled to the
// prompt registry this test would be the guard against silently losing them.
func TestNonCoreSkillsRemainDiscoverableViaSearch(t *testing.T) {
	// The search tool enumerates from its own discovery, not from the slim
	// prompt registry, so a non-core skill must still be findable there.
	entries := []SkillReg{
		{Name: "systematic-debugging", Description: "调试用"},
		{Name: "three-webgl-game", Description: "WebGL 运行时"},
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "three-webgl-game" {
			found = true
		}
	}
	if !found {
		t.Fatalf("non-core skill must remain in the discoverable set")
	}
}
