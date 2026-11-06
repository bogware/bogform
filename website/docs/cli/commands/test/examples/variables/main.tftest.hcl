# First, set the variable here:
variables {
  name = "Bogform"
}

run "basic" {
  assert {
    condition     = output.greeting == "Hello Bogform!"
    error_message = "Incorrect greeting: ${output.greeting}"
  }
}

run "override" {
  # Override it for this test case only here:
  variables {
    name = "Bogform user"
  }
  assert {
    condition     = output.greeting == "Hello Bogform user!"
    error_message = "Incorrect greeting: ${output.greeting}"
  }
}