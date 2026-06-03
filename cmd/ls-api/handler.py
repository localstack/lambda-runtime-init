import json


def handler(event, context):
    print(f"Received: {json.dumps(event)}")
    return {"statusCode": 200, "body": json.dumps({"echo": event})}
