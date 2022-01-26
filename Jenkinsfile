@Library('vega-shared-library') _

/* properties of scmVars (example):
    - GIT_BRANCH:PR-40-head
    - GIT_COMMIT:05a1c6fbe7d1ff87cfc40a011a63db574edad7e6
    - GIT_PREVIOUS_COMMIT:5d02b46fdb653f789e799ff6ad304baccc32cbf9
    - GIT_PREVIOUS_SUCCESSFUL_COMMIT:5d02b46fdb653f789e799ff6ad304baccc32cbf9
    - GIT_URL:https://github.com/vegaprotocol/notion-data-service.git
*/
def scmVars = null
def version = 'UNKNOWN'
def versionHash = 'UNKNOWN'
def commitHash = 'UNKNOWN'


pipeline {
    agent any
    options {
        skipDefaultCheckout true
        timestamps()
        timeout(time: 45, unit: 'MINUTES')
    }
    environment {
        GO111MODULE = 'on'
        CGO_ENABLED  = '0'
        DOCKER_IMAGE_TAG_LOCAL = "j-${ env.JOB_BASE_NAME.replaceAll('[^A-Za-z0-9\\._]','-') }-${BUILD_NUMBER}-${EXECUTOR_NUMBER}"
        DOCKER_IMAGE_NAME_LOCAL = "ghcr.io/vegaprotocol/notion-data-service/notion-data-service:${DOCKER_IMAGE_TAG_LOCAL}"
    }

    stages {
        stage('Config') {
            steps {
                cleanWs()
                sh 'printenv'
                echo "${params}"
            }
        }
        stage('Git Clone') {
            options { retry(3) }
            steps {
                retry(3) {
                        checkout scm
                }
            }
        }

        stage('Dependencies') {
            options { retry(3) }
            steps {
                sh 'pwd'
            }
        }

        stage('Build docker image') {
            options { retry(3) }
            steps {
                withDockerRegistry([credentialsId: 'github-vega-ci-bot-artifacts', url: "https://ghcr.io"]) {
                    sh label: 'Build docker image', script: '''
                        docker build -t "${DOCKER_IMAGE_NAME_LOCAL}" .
                    '''
                }
            }
        }
        stage('Publish docker image') {
            when {
                anyOf {
                    buildingTag()
                    branch 'develop'
                    // changeRequest() // uncomment only for testing
                }
            }
            environment {
                DOCKER_IMAGE_TAG_VERSIONED = "${ env.TAG_NAME ? env.TAG_NAME : env.BRANCH_NAME }"
                DOCKER_IMAGE_NAME_VERSIONED = "ghcr.io/vegaprotocol/data-node/data-node:${DOCKER_IMAGE_TAG_VERSIONED}"
                DOCKER_IMAGE_TAG_ALIAS = "${ env.TAG_NAME ? 'latest' : 'edge' }"
                DOCKER_IMAGE_NAME_ALIAS = "ghcr.io/vegaprotocol/data-node/data-node:${DOCKER_IMAGE_TAG_ALIAS}"
            }
            options { retry(3) }
            steps {
                sh label: 'Tag new images', script: '''#!/bin/bash -e
                    docker image tag "${DOCKER_IMAGE_NAME_LOCAL}" "${DOCKER_IMAGE_NAME_VERSIONED}"
                    docker image tag "${DOCKER_IMAGE_NAME_LOCAL}" "${DOCKER_IMAGE_NAME_ALIAS}"
                '''

                withDockerRegistry([credentialsId: 'github-vega-ci-bot-artifacts', url: "https://ghcr.io"]) {
                    sh label: 'Push docker images', script: '''
                        docker push "${DOCKER_IMAGE_NAME_VERSIONED}"
                        docker push "${DOCKER_IMAGE_NAME_ALIAS}"
                    '''
                }
            }
        }
    }
}
