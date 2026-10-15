# Run a coding agent with builtin components

English | [中文](README.zh.md)

Generate a calculator in an empty working directory, then continue the same Session to change it and add tests. This example uses builtin model access, file tools, JSONL and headless, without custom plugins. See [application concepts](../../docs/concepts.md) for the component relationships.

First complete the build and model setup in [getting started](../../docs/getting-started.md), preparing `dist/coding-agent/qi`, `profile.yaml` and `workspace/`. Continue below from the repository root. Running the generated tests at the end requires Python 3.

## Generate code

Enter the working directory and submit a task. `--json` prints the Session ID and execution events so you can continue the same Session in the next step:

```sh
cd dist/coding-agent/workspace
../qi --profile ../profile.yaml -- --json \
  'Use write_file to create calculator.py with add, subtract, multiply and divide functions, then use read_file to check it.'
```

Check the exit code and inspect the four functions in `calculator.py`. Copy `sessionId` from the event whose `type` is `session`, then replace `session-...` below. Each invocation creates a new Session unless you pass `--session-id`.

## Continue the same Session

```sh
../qi --profile ../profile.yaml -- --session-id 'session-...' \
  'Read the calculator you just created and update divide to raise ValueError when the divisor is zero. Create test_calculator.py using Python unittest to cover all four operations and division by zero, then read both files back and summarize the changes.'
```

History is saved in `.qi/coding-agent/sessions` under the working directory. Keep the same working and storage directories when resuming.

## Inspect changes and run tests

Inspect both files. `write_file` completely overwrites existing files, so read them before modifying them. This example supplies only file read/write tools; run the tests yourself in the same working directory:

```sh
python3 -m unittest -v test_calculator
```

Passing tests establish that these cases verify the expected behavior. The model's summary or normal turn completion does not replace testing. Keep `dist/coding-agent/` to continue the exercise. See [Headless](../../cli/headless/README.md) for options, JSON and resume limits.

See [file tools](../../tools/fs/README.md), [OpenAI-compatible](../../llm/openai/README.md) and [JSONL Sessions](../../session/jsonl/README.md) for configuration details, or the [building guide](https://github.com/RussellLuo/qi/tree/main/kernel/docs/building.md) to use qi-build. Continue with the [custom tool plugin](../tool-plugin/README.md) and [process tool](../tool-process/README.md).
