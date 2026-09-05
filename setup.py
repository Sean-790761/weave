#!/usr/bin/env python3
"""
Setup script for weave Python SDK
"""

from setuptools import setup, find_packages

with open("README.md", "r", encoding="utf-8") as fh:
    long_description = fh.read()

setup(
    name="weave-sdk",
    version="1.1.0",
    author="Sean-790761",
    description="Python SDK for Weave agent execution framework",
    long_description=long_description,
    long_description_content_type="text/markdown",
    url="https://github.com/Sean-790761/weave",
    packages=find_packages(where="sdk"),
    package_dir={"": "sdk"},
    classifiers=[
        "Development Status :: 4 - Beta",
        "Intended Audience :: Developers",
        "License :: OSI Approved :: MIT License",
        "Operating System :: OS Independent",
        "Programming Language :: Python :: 3",
        "Programming Language :: Python :: 3.8",
        "Programming Language :: Python :: 3.9",
        "Programming Language :: Python :: 3.10",
        "Programming Language :: Python :: 3.11",
        "Programming Language :: Python :: 3.12",
    ],
    python_requires=">=3.8",
    install_requires=[
        # Add dependencies here if needed
    ],
    extras_require={
        "dev": [
            "pytest>=6.0",
            "pytest-cov>=2.0",
            "black>=22.0",
            "flake8>=4.0",
            "mypy>=0.910",
        ],
    },
    entry_points={
        "console_scripts": [
            "weave-reviewer=examples.reviewer:main",
        ],
    },
    include_package_data=True,
    zip_safe=False,
)