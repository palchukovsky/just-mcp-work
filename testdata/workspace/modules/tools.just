set windows-shell := ["powershell.exe", "-NoLogo", "-Command"]

alias c := check

[group('quality')]
[confirm('Run check?')]
check target="all":
    @echo "check {{target}}"

mod nested
