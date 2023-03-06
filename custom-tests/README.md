# Testing lambda-runtime-init

## Testing in isolation
Useful if you want more control over the API between the init and LocalStack (e.g. for error responses)


## Debugging with LocalStack

1. Build init via `make build`
    * On ARM hosts, use `make ARCH=arm64 build`

2. Start LocalStack with the following flags:

    ```
    LAMBDA_INIT_BIN_PATH=/Users/joe/Projects/LocalStack/lambda-runtime-init/custom-tests/init/var/rapid/init
    LAMBDA_INIT_BOOTSTRAP_PATH=/Users/joe/Projects/LocalStack/lambda-runtime-init/custom-tests/init/var/rapid/entrypoint.sh
    LAMBDA_INIT_DEBUG=1
    LAMBDA_INIT_DELVE_PATH=/Users/joe/Projects/LocalStack/lambda-runtime-init/custom-tests/init/var/rapid/dlv
    LAMBDA_INIT_DELVE_PORT=40000
    LAMBDA_RUNTIME_ENVIRONMENT_TIMEOUT=3600
    TEST_DISABLE_RETRIES_AND_TIMEOUTS=1
    ```

   * `LAMBDA_INIT_DEBUG=1|0` enables or disables RIE copying and debugging.
   * `LAMBDA_REMOVE_CONTAINERS=0` keeps exited containers
   * Adjust the path to `lambda-runtime-init` accordingly

3. Start Delve debugger and connect to `localhost:40000`


### Advice for function configuration

Within `create_lambda_function`:

* Increase the `timeout=3600`
* On ARM hosts, use `Architectures=[Architecture.arm64]`
