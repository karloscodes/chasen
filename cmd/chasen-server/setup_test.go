package main

import "testing"

func TestPortsTaken(t *testing.T) {
	t.Run("names the program on each port, once", func(t *testing.T) {
		ss := `LISTEN 0      511          0.0.0.0:80        0.0.0.0:*    users:(("nginx",pid=812,fd=6),("nginx",pid=811,fd=6))
LISTEN 0      511             [::]:80           [::]:*    users:(("nginx",pid=812,fd=7),("nginx",pid=811,fd=7))
LISTEN 0      4096         0.0.0.0:443       0.0.0.0:*    users:(("apache2",pid=900,fd=4))`

		said := listeners(ss)

		if said != "Port 80 is taken by nginx, and port 443 is taken by apache2" {
			t.Errorf("listeners = %q", said)
		}
	})

	t.Run("free ports say nothing", func(t *testing.T) {
		if said := listeners(""); said != "" {
			t.Errorf("listeners = %q, want nothing", said)
		}
	})
}
