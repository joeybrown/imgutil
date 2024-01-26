- I was running into issues with docker_registry.go while running `./acceptance/reproducibility_test.go`.
  - I found this thread and followed the advice: https://cloud-native.slack.com/archives/C0331B5QS02/p1698698521881809?thread_ts=1698697982.090649&cid=C0331B5QS02
  - I'm running a registry outside of the tests and hardcoding the path to that registry...

  > docker run -d -p 5000:5000 --name registry registry:2.7

  ```
  func newTestImageName() string {
	  return "localhost" + ":" + "5000" + "/imgutil-acceptance-" + h.RandString(10)
  }
  ```
