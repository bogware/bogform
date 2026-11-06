run "test" {
  module {
    source="../"
  }
  variables {
    name = "Bogform"
  }
  assert {
    condition = output.greeting == "Hello Bogform!"
    error_message = "Incorrect greeting: ${output.greeting}"
  }
}