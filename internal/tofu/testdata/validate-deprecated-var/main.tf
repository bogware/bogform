// When calling a module and providing a value for a deprecated variable, Bogform should warn about that
module "foo-call" {
  source = "./modfoo"
  foo    = "foo given value"
}

// When calling a module and NOT providing a value for a deprecated variable, Bogform should skip warning about it
module "foo-call-no-var" {
  source = "./modfoo"
}

// When calling a module and providing a NULL value for a deprecated variable, Bogform should skip warning about it
// This is due to the way Bogform handles null values.
// For more information, check the considerations about the `null` value from the docs: https://bogware.github.io/bogform/docs/language/expressions/types/#types
module "foo-call-null" {
  source = "./modfoo"
  bar    = null
}
