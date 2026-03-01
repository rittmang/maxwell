package queue

import "testing"

func TestRebindPostgres(t *testing.T) {
	q := `INSERT INTO t(a,b,c) VALUES (?, ?, ?) WHERE id=?`
	got := rebindPostgres(q)
	want := `INSERT INTO t(a,b,c) VALUES ($1, $2, $3) WHERE id=$4`
	if got != want {
		t.Fatalf("unexpected rebound query:\nwant: %s\n got: %s", want, got)
	}
}

func TestMapDriver(t *testing.T) {
	cases := map[string]string{
		"sqlite":   "sqlite",
		"postgres": "pgx",
		"mysql":    "mysql",
		"":         "sqlite",
	}
	for in, want := range cases {
		if got := mapDriver(in); got != want {
			t.Fatalf("mapDriver(%q)=%q want %q", in, got, want)
		}
	}
}
