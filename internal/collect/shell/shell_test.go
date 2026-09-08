package shell

import "testing"

func TestParse(t *testing.T) {
	cases := []struct{ in, bin, sub string }{
		{"ls -la", "ls", ""},
		{"git commit -m 'secret message'", "git", "commit"},
		{"git -C /x status", "git", ""},
		{"ddev drush cr", "ddev", "drush"},
		{"vendor/bin/drush sql:dump --gzip", "vendor/bin/drush", "sql:dump"},
		{"/usr/bin/php artisan migrate", "php", ""},
		{"sudo dnf install foo", "dnf", "install"},
		{"sudo -E env FOO=bar composer install", "composer", "install"},
		{"FOO=bar BAZ=1 make build", "make", "build"},
		{"mysql -uroot -pSECRET db", "mysql", ""},
		{"curl -H 'Authorization: Bearer x' https://a", "curl", ""},
		{"cat file | grep x", "cat", ""},
		{"cd ~/dev && git pull", "cd", ""},
		{"echo hi > out.txt", "echo", ""},
		{"go test ./...", "go", "test"},
		{"go run ./cmd/x", "go", "run"},
		{"docker compose up -d", "docker", "compose"},
		{"npm run build", "npm", "run"},
		{"git 123", "git", ""},
		{"", "", ""},
		{"FOO=bar", "", ""},
		{"time", "", ""},
	}
	for _, c := range cases {
		bin, sub := Parse(c.in)
		if bin != c.bin || sub != c.sub {
			t.Errorf("Parse(%q) = %q,%q want %q,%q", c.in, bin, sub, c.bin, c.sub)
		}
	}
}
