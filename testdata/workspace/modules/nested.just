set windows-shell := ["powershell.exe", "-NoLogo", "-Command"]

nested-task value="nested":
    @echo {{value}}
