# Test: Home Directory Fix (Node.js 22, --user=1000:1000)

## Step 1 — Build the binary

```bash
make compile-with-docker
# produces bin/aws-lambda-rie-x86_64
```

## Step 2 — Create the function file

```bash
mkdir -p /tmp/fn-homedir
cat > /tmp/fn-homedir/index.js << 'EOF'
exports.handler = async () => {
  const os = require("os");
  const homeEnv = process.env.HOME;
  const debug = {
    HOME_env: homeEnv === undefined ? null : homeEnv,
    has_HOME_key: Object.prototype.hasOwnProperty.call(process.env, "HOME"),
  };
  try {
    const h = os.homedir();
    return { statusCode: 200, body: JSON.stringify({ ok: true, ...debug, homedir: h }) };
  } catch (e) {
    return {
      statusCode: 500,
      body: JSON.stringify({
        ok: false, ...debug,
        name: e.name, code: e.code, message: e.message,
        syscall: e.syscall, errno: e.errno, info: e.info,
      }),
    };
  }
};
EOF
```

## Step 3 — Start LocalStack with your binary and --user=1000:1000

The binary path must be mounted into the LocalStack container via `DOCKER_FLAGS`, then
referenced via `LAMBDA_INIT_BIN_PATH` using the in-container path.

```bash
DOCKER_FLAGS="-v $(pwd)/bin:/lambda-bin" \
LAMBDA_DOCKER_FLAGS="--user=1000:1000" \
LAMBDA_INIT_BIN_PATH=/lambda-bin/aws-lambda-rie-x86_64 \
localstack start -d

localstack wait -t 60
```

> **Note:** LocalStack warns to use `LOCALSTACK_`-prefixed env vars — both forms work.

## Step 4 — Deploy and invoke

```bash
# Zip the function
cd /tmp/fn-homedir && zip function.zip index.js && cd -

# Create the function
awslocal lambda create-function \
  --function-name homedir-test \
  --runtime nodejs22.x \
  --handler index.handler \
  --role arn:aws:iam::000000000000:role/lambda-role \
  --zip-file fileb:///tmp/fn-homedir/function.zip

# Wait for it to be active
awslocal lambda wait function-active --function-name homedir-test

# Invoke and pretty-print the response
awslocal lambda invoke \
  --function-name homedir-test \
  --payload '{}' \
  /tmp/fn-homedir/response.json && cat /tmp/fn-homedir/response.json
```

## Expected output (with the EnsureHome() fix)

```json
{"statusCode":200,"body":"{\"ok\":true,\"HOME_env\":\"/tmp\",\"has_HOME_key\":true,\"homedir\":\"/tmp\"}"}
```

Without the fix you'd get `statusCode: 500` with `"code":"ENOENT"`.
