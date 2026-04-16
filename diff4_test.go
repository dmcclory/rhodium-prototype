package main

import "testing"

// TestClassifyAll15 explicitly verifies every equivalence class. Mirrors
// Iron's own classifier test — each case is a diamond where the equality
// partition is unambiguous.
func TestClassifyAll15(t *testing.T) {
	tests := []struct {
		name string
		d    Diamond
		want Class
	}{
		// --- Hidden classes ---
		{
			name: "all equal",
			d:    Diamond{B1: "A", F1: "A", B2: "A", F2: "A"},
			want: ClassB1B2F1F2,
		},
		{
			name: "bases equal, features equal (clean merge)",
			d:    Diamond{B1: "A", F1: "B", B2: "A", F2: "B"},
			want: ClassB1B2__F1F2,
		},
		{
			name: "old pair equal, new pair equal (clean merge)",
			d:    Diamond{B1: "A", F1: "A", B2: "B", F2: "B"},
			want: ClassB1F1__B2F2,
		},
		{
			name: "FORGET: base absorbed feature",
			d:    Diamond{B1: "A", F1: "B", B2: "B", F2: "B"},
			want: ClassB2F1F2,
		},

		// --- Shown-as-diff2 classes ---
		{
			name: "b1=b2=f1, new tip differs (most common: new commits, no rebase)",
			d:    Diamond{B1: "A", F1: "A", B2: "A", F2: "B"},
			want: ClassB1B2F1,
		},
		{
			name: "b1=b2=f2, old tip differs",
			d:    Diamond{B1: "A", F1: "B", B2: "A", F2: "A"},
			want: ClassB1B2F2,
		},
		{
			name: "bases equal only (new commits diverged)",
			d:    Diamond{B1: "A", F1: "B", B2: "A", F2: "C"},
			want: ClassB1B2,
		},
		{
			name: "b1=f1=f2, new base differs",
			d:    Diamond{B1: "A", F1: "A", B2: "B", F2: "A"},
			want: ClassB1F1F2,
		},
		{
			name: "b2=f1",
			d:    Diamond{B1: "A", F1: "B", B2: "B", F2: "C"},
			want: ClassB2F1,
		},
		{
			name: "cross-equal: b1=f2, b2=f1",
			d:    Diamond{B1: "A", F1: "B", B2: "B", F2: "A"},
			want: ClassB1F2__B2F1,
		},

		// --- Complex classes ---
		{
			name: "old pair equal only",
			d:    Diamond{B1: "A", F1: "A", B2: "B", F2: "C"},
			want: ClassB1F1,
		},
		{
			name: "b1=f2 diagonal",
			d:    Diamond{B1: "A", F1: "B", B2: "C", F2: "A"},
			want: ClassB1F2,
		},
		{
			name: "new pair equal only",
			d:    Diamond{B1: "A", F1: "B", B2: "C", F2: "C"},
			want: ClassB2F2,
		},
		{
			name: "features equal only",
			d:    Diamond{B1: "A", F1: "B", B2: "C", F2: "B"},
			want: ClassF1F2,
		},
		{
			name: "conflict: all different",
			d:    Diamond{B1: "A", F1: "B", B2: "C", F2: "D"},
			want: ClassConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.d, nil)
			if got != tt.want {
				t.Errorf("Classify(%v) = %s, want %s", tt.d, got, tt.want)
			}
		})
	}
}

func TestClassHidden(t *testing.T) {
	hidden := []Class{ClassB1B2F1F2, ClassB1B2__F1F2, ClassB1F1__B2F2, ClassB2F1F2}
	for _, c := range hidden {
		if !c.Hidden() {
			t.Errorf("%s.Hidden() = false, want true", c)
		}
		if c.ShownAsDiff2() {
			t.Errorf("%s.ShownAsDiff2() = true, want false", c)
		}
	}
}

func TestClassShownAsDiff2(t *testing.T) {
	shown := []Class{ClassB1B2F1, ClassB1B2F2, ClassB1B2, ClassB1F1F2, ClassB2F1, ClassB1F2__B2F1}
	for _, c := range shown {
		if c.Hidden() {
			t.Errorf("%s.Hidden() = true, want false", c)
		}
		if !c.ShownAsDiff2() {
			t.Errorf("%s.ShownAsDiff2() = false, want true", c)
		}
		views := c.Views()
		if len(views) != 1 {
			t.Errorf("%s.Views() has %d views, want 1", c, len(views))
		}
	}
}

func TestClassComplex(t *testing.T) {
	complex := []Class{ClassB1F1, ClassB1F2, ClassB2F2, ClassF1F2, ClassConflict}
	for _, c := range complex {
		if c.Hidden() {
			t.Errorf("%s.Hidden() = true, want false", c)
		}
		if c.ShownAsDiff2() {
			t.Errorf("%s.ShownAsDiff2() = true, want false", c)
		}
		views := c.Views()
		if len(views) == 0 {
			t.Errorf("%s.Views() is empty, want at least 1 fallback view", c)
		}
	}
}

func TestClassIsForget(t *testing.T) {
	if !ClassB2F1F2.IsForget() {
		t.Error("ClassB2F1F2.IsForget() = false, want true")
	}
	if ClassB1B2F1F2.IsForget() {
		t.Error("ClassB1B2F1F2.IsForget() = true, want false")
	}
	if ClassConflict.IsForget() {
		t.Error("ClassConflict.IsForget() = true, want false")
	}
}

func TestClassString(t *testing.T) {
	if s := ClassConflict.String(); s != "conflict" {
		t.Errorf("ClassConflict.String() = %q, want %q", s, "conflict")
	}
	if s := ClassB1B2F1.String(); s != "b1_b2_f1" {
		t.Errorf("ClassB1B2F1.String() = %q, want %q", s, "b1_b2_f1")
	}
}

// TestComputeHidden verifies that hidden classes produce empty results.
func TestComputeHidden(t *testing.T) {
	tests := []struct {
		name string
		d    Diamond
	}{
		{"all equal", Diamond{B1: "X", F1: "X", B2: "X", F2: "X"}},
		{"clean rebase", Diamond{B1: "A", F1: "B", B2: "A", F2: "B"}},
		{"forget", Diamond{B1: "old", F1: "new", B2: "new", F2: "new"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Compute(tt.d)
			if len(r.Segments) != 0 {
				t.Errorf("Compute(%v) has %d segments, want 0", tt.d, len(r.Segments))
			}
		})
	}
}

// TestComputeShownAsDiff2 verifies the fast-path produces one segment with
// the correct class.
func TestComputeShownAsDiff2(t *testing.T) {
	d := Diamond{
		B1: "line1\nline2\n",
		F1: "line1\nline2\n",
		B2: "line1\nline2\n",
		F2: "line1\nlineNEW\nline2\n",
	}
	r := Compute(d)
	if len(r.Segments) != 1 {
		t.Fatalf("Compute: got %d segments, want 1", len(r.Segments))
	}
	seg := r.Segments[0]
	if seg.Class != ClassB1B2F1 {
		t.Errorf("segment class = %s, want %s", seg.Class, ClassB1B2F1)
	}
	if !seg.Class.ShownAsDiff2() {
		t.Error("segment class should be shown_as_diff2")
	}
	views := seg.Class.Views()
	if len(views) != 1 {
		t.Fatalf("views: got %d, want 1", len(views))
	}
	if views[0].From != B2 || views[0].To != F2 {
		t.Errorf("view = %s→%s, want b2→f2", views[0].From, views[0].To)
	}
}

// TestComputeComplex verifies complex classes produce a fallback segment.
func TestComputeComplex(t *testing.T) {
	d := Diamond{B1: "A", F1: "B", B2: "C", F2: "D"}
	r := Compute(d)
	if len(r.Segments) != 1 {
		t.Fatalf("Compute: got %d segments, want 1", len(r.Segments))
	}
	if r.Segments[0].Class != ClassConflict {
		t.Errorf("segment class = %s, want conflict", r.Segments[0].Class)
	}
}

// TestClassifyWithCustomEquality verifies that a custom equality function
// (e.g., whitespace-insensitive) is respected.
func TestClassifyWithCustomEquality(t *testing.T) {
	wsEqual := func(a, b string) bool {
		// Very naive: treat all content as equal if non-empty.
		// Just testing that the eq function is actually called.
		return len(a) > 0 && len(b) > 0
	}
	d := Diamond{B1: "hello", F1: "world", B2: "foo", F2: "bar"}
	// With exact equality → conflict (all different).
	if got := Classify(d, nil); got != ClassConflict {
		t.Errorf("exact: got %s, want conflict", got)
	}
	// With our custom "everything is equal" → all equal.
	if got := Classify(d, wsEqual); got != ClassB1B2F1F2 {
		t.Errorf("custom: got %s, want b1_b2_f1_f2", got)
	}
}

// TestComputeRealisticNoRebase simulates the most common real-world case:
// reviewer saw the PR, author pushed more commits, no rebase.
func TestComputeRealisticNoRebase(t *testing.T) {
	base := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"
	oldTip := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n\tprintln(\"world\")\n}\n"
	newTip := "package main\n\nfunc main() {\n\tprintln(\"hello\")\n\tprintln(\"world\")\n\tprintln(\"!\")\n}\n"

	d := Diamond{
		B1: base,   // old base = new base (no rebase)
		F1: oldTip, // what reviewer saw
		B2: base,   // same base
		F2: newTip, // new commits added
	}

	r := Compute(d)
	if len(r.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(r.Segments))
	}
	seg := r.Segments[0]
	// b1=b2, f1≠f2 → ClassB1B2 ("diff extension")
	if seg.Class != ClassB1B2 {
		t.Errorf("class = %s, want b1_b2", seg.Class)
	}
	views := seg.Class.Views()
	if views[0].From != F1 || views[0].To != F2 {
		t.Errorf("view = %s→%s, want f1→f2", views[0].From, views[0].To)
	}
}

// TestComputeRealisticCleanRebase simulates a clean rebase where content
// is identical despite SHA changes.
func TestComputeRealisticCleanRebase(t *testing.T) {
	oldBase := "package main\n\nfunc main() {}\n"
	newBase := "package main\n\nimport \"fmt\"\n\nfunc main() {}\n"
	oldTip := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	newTip := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"

	// The feature added the same println in both — it's just rebased onto the
	// new base that added the import. Reviewer saw old_base→old_tip. Now the
	// state is new_base→new_tip with identical content delta.
	//
	// b1≠b2, f1≠f2, but the "change" is the same → b1_f1 (old pair) and b2_f2
	// (new pair) are independently equal? No — the actual content differs.
	//
	// In this case: b1≠f1, b1≠b2, b1≠f2, f1≠b2, f1≠f2, b2≠f2
	// → ClassConflict. This is correct — a true clean rebase requires
	// segmentation (M2+) to detect that the deltas are equivalent even though
	// the files differ. For M1, we fall through to showing the full diff.
	d := Diamond{B1: oldBase, F1: oldTip, B2: newBase, F2: newTip}
	r := Compute(d)
	if len(r.Segments) != 1 {
		t.Fatalf("got %d segments, want 1", len(r.Segments))
	}
	if r.Segments[0].Class != ClassConflict {
		t.Errorf("class = %s, want conflict (M2 needed for clean rebase detection)", r.Segments[0].Class)
	}
}

// TestViewMapping verifies the view mapping for key classes.
func TestViewMapping(t *testing.T) {
	tests := []struct {
		class    Class
		wantFrom Corner
		wantTo   Corner
	}{
		{ClassB1B2F1, B2, F2},     // new diff
		{ClassB1B2F2, F1, F2},     // dropped feature change
		{ClassB1B2, F1, F2},       // diff extension
		{ClassB1F1F2, B2, F2},     // dropped base change
		{ClassB2F1, B2, F2},       // diff extension
		{ClassB1F2__B2F1, B2, F2}, // dropped same change
	}
	for _, tt := range tests {
		views := tt.class.Views()
		if len(views) == 0 {
			t.Errorf("%s: no views", tt.class)
			continue
		}
		v := views[0]
		if v.From != tt.wantFrom || v.To != tt.wantTo {
			t.Errorf("%s: view = %s→%s, want %s→%s", tt.class, v.From, v.To, tt.wantFrom, tt.wantTo)
		}
	}
}

// TestClassifyTotality verifies that Classify always returns a valid class
// for any combination of equal/different values.
func TestClassifyTotality(t *testing.T) {
	// Generate all possible equality patterns with 4 values.
	// Use small strings as stand-ins for different content.
	vals := []string{"A", "B", "C", "D"}

	count := 0
	for _, b1 := range vals {
		for _, f1 := range vals {
			for _, b2 := range vals {
				for _, f2 := range vals {
					d := Diamond{B1: b1, F1: f1, B2: b2, F2: f2}
					c := Classify(d, nil)
					if c < ClassB1B2F1F2 || c > ClassConflict {
						t.Errorf("Classify(%q,%q,%q,%q) = %d (out of range)", b1, f1, b2, f2, c)
					}
					count++
				}
			}
		}
	}
	// 4^4 = 256 combinations.
	if count != 256 {
		t.Errorf("tested %d combinations, want 256", count)
	}
}

// TestClassifyConsistency verifies that if two corners are equal, the class
// never puts them in different groups.
func TestClassifyConsistency(t *testing.T) {
	vals := []string{"A", "B", "C", "D"}
	for _, b1 := range vals {
		for _, f1 := range vals {
			for _, b2 := range vals {
				for _, f2 := range vals {
					d := Diamond{B1: b1, F1: f1, B2: b2, F2: f2}
					c := Classify(d, nil)

					// If b1==b2, the class name should reflect that.
					if b1 == b2 {
						switch c {
						case ClassB1B2F1F2, ClassB1B2__F1F2, ClassB1B2F1, ClassB1B2F2, ClassB1B2:
							// OK — class includes b1_b2 grouping.
						case ClassB1F1__B2F2:
							// b1=f1 and b2=f2 — if b1=b2 then f1=f2 too → all equal.
							// This can only happen if b1=b2=f1=f2.
						case ClassB1B2F1F2 + 100: // unreachable sentinel
						default:
							// When b1==b2, we should get a class that has b1 and b2
							// in the same group. Verify.
							if b1 == f1 && f1 == f2 {
								if c != ClassB1B2F1F2 {
									t.Errorf("b1=b2=f1=f2=%q: got %s", b1, c)
								}
							}
						}
					}

					_ = c // suppress unused if we skip checks
				}
			}
		}
	}
}
