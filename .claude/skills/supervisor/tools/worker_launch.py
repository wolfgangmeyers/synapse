#!/usr/bin/env python3
"""Compatibility entry point for the active tmux worker launcher."""
import os
import runpy

root = os.path.abspath(os.path.join(os.path.dirname(os.path.realpath(__file__)), '..', '..', '..', '..'))
runpy.run_path(os.path.join(root, 'tools', 'tmux-worker', 'worker_launch.py'), run_name='__main__')
