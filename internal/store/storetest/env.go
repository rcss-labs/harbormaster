package storetest

import "os"

func lookupURL() string { return os.Getenv("HM_TEST_DATABASE_URL") }
