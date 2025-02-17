# Overview
This is an insanely skunkworks type project. Everything here is experimental and most likely unstable.
Will use this as the base for my Olympus project.


# Personal Notes


These two statements are equivilent. The differences are that the bottom conditional is more concise and that the err variable is exclusive to the conditionals scope.
```go
////////////////////////////////////////////////////////
err := http.ListenAndServe(port, nil)
if err != nil {
    log.Fatalf("Error starting server: %v\n", err)
}
////////////////////////////////////////////////////////
if err := http.ListenAndServe(port, nil); err != nil {
    log.Fatalf("Error starting server: %v\n", err)
}
////////////////////////////////////////////////////////
```