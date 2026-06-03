import json


def handler(event, context):
    print(f"Received: {json.dumps(event)}")
    if event.get("fail"):
        raise Exception(f"Intentional failure: fail={event['fail']}")
    return {"statusCode": 200, "body": json.dumps({"echo": event})}
