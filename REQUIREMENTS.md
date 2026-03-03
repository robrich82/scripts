I have these bookmarks (bookmarks.html), I currently use them to manually check if software I have has updates for things I don't believe I have auto-updaters or the ability to update via winget, then I manually download and install them.

I would like to automate this process

1. Check what the latest version is available and compare to local
2. Download the update
3. Notify what is available, If feasible, prompt to install the update(s) and the result (in case it failed etc.)

Now some of these should be relatively straight forward, the GitHub release page tells us the latest release. However some we want beta, others we don't. On top of that others have custom pages I will need to explain how to check the version.

For the local install, I might need to tell you where it is installed, then we'll need to try determine the current version, if it isn't obvious then I might need to help you.

We should be using GoLang.

We should also have a way to add/remove applications from this list.
