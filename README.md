# Overview
This is an insanely skunkworks type project. Everything here is experimental and most likely unstable.
Will use this as the base for my Olympus project.

# Matchmaking Service
The matchmaking service will mimic a stripped down version of the League of Legends match maker. It will listen to connections that come in from player clients, once enough players have connected it will match them up based off of a primitive "Anchor Being" methodology. Nine players plus the originating player will form the 10 man lobby and the team player names will be sent back to the participating players.

## Player Selection Method - "Anchor Being"
At the moment there is no complicated matching algorithm in place being used to match players. Each player will search for their own set of team mates, essentially treating themselves as the anchor. Once they are finishes combing through the Redis database for matching possible teammates we notify all selected parties that they have been selected and send them whatever we need to.

## Deploy
There is a dockerized build of the application. The main `Dockerfile` is used for building the GO server itself and it is triggered from the docker-compose file. The docker-compose file will deploy the Redis database as well as the GO matchmaking server. 

To deploy the matchmaking service just run the following command in the projects root directory
```bash
docker-compose up
```
> **Note:** You might have to run `docker-compose up --build` if you have having problems with old builds being cached. 


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