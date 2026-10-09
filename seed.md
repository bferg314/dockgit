dockgit
A TUI for combining your docker containers with your git workflow written in go, using the design choices from folgit.

First tab is all your docker stuff, running images, etc. You can easily view logs and stuff from here as well.
Second tab is your local git repos that use docker. This makes it one click to switch the branch you are looking at and run docker compose up -d -build OR get the latest from the branch you are on and build. Also supports auto build cache cleanup. The repos are added individually by folder.
Third tab points at your docker composes repo, a repository specific to docker composes, and shows all the yaml files in it and their paths, and allows you to get latest, launch your composes, update your env files, etc.
Fourth tab is your settings.

There should be some intelligence between the docker images tab and the other two.