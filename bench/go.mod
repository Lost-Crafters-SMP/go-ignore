module go.lostcrafters.com/ignore/bench

go 1.27.1

replace go.lostcrafters.com/ignore => ../

require (
	github.com/git-pkgs/gitignore v1.3.0
	github.com/sabhiram/go-gitignore v0.0.0-20210923224102-525f6e181f06
	go.lostcrafters.com/ignore v0.0.0-00010101000000-000000000000
)

require (
	github.com/aclements/go-moremath v0.0.0-20210112150236-f10218a38794 // indirect
	golang.org/x/perf v0.0.0-20260929162123-406019bb8b68 // indirect
)

tool golang.org/x/perf/cmd/benchstat
