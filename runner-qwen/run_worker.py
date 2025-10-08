#!/usr/bin/env python
"""Entry point script to run the queue worker."""
import asyncio
import sys
import os

# Add debug logging before any imports
print("run_worker.py: Starting...", flush=True)

from app.worker import main

print("run_worker.py: Imported main, calling it now...", flush=True)

if __name__ == "__main__":
    asyncio.run(main())
