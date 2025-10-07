#!/usr/bin/env python
"""Entry point script to run the queue worker."""
import asyncio
from app.worker import main

if __name__ == "__main__":
    asyncio.run(main())
